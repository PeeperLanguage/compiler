//! Checking a function body into THIR: its statements, and the names its
//! blocks declare.

mod assignments;
mod loops;

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ConstArguments;
use model::DeclarationCopy;
use model::constant::Value;
use model::semantic_types::ConstArgument;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Block;
use model::thir::Body;
use model::thir::ExprKind;
use model::thir::FactKind;
use model::thir::Local;
use model::thir::Stmt;
use model::thir::Symbol;
use model::thir::Variable;
use source::Span;
use tree::ast;
use tree::ast::Ident;

use crate::Checker;
use crate::Checking;
use crate::builder::Declared;
use crate::builder::ThirBuilder;
use crate::scope::report_redeclared;

impl Checker<'_> {
    /// Puts in `body` the THIR of the body of the function `copy`,
    /// asked for at `used_at`, recording its facts when `records_facts`
    /// (`Checking`), with `has_errors` set when it has an error (`Body`);
    /// what `body` held is dropped, its memory kept. False, with `body`
    /// untouched, for a function without a body or whose signature had
    /// an error. A function a copy is made for, given nothing, is checked
    /// as its signature is, each type parameter standing for itself and no
    /// value for a `const` one (`own_arguments`): what its body names is
    /// found, and it reports what is wrong whatever those are.
    pub fn check_body(
        &mut self,
        copy: DeclarationCopy,
        used_at: Span,
        body: &mut Body,
        records_facts: bool,
    ) -> bool {
        let DeclarationCopy {
            definition,
            arguments,
        } = copy;
        let function = model::program::function_declaration(self.modules, definition);
        let Some(block) = function.body else {
            return false;
        };
        let ty = self.copy_type(copy, used_at);
        let module = definition.module.index();
        let reported = self.module_checks[module].diagnostics.len();
        let Type::Function {
            parameters, result, ..
        } = &self.types[ty]
        else {
            return false; // the signature's error was reported
        };
        let (parameters, result) = (parameters.clone(), *result);
        let tree = &self.modules[module].ast;
        let arguments = match arguments {
            ConstArguments::NONE => self.own_arguments(definition),
            copy => copy,
        };
        let checking = Checking {
            definition,
            arguments,
            used_at,
            records_facts,
        };
        self.checking_in(checking, |checker| {
            body.clear();
            let mut builder = ThirBuilder::new(checker, body, result);
            let outermost = builder.body.add(
                Block {
                    parent: None,
                    statements: List::EMPTY,
                },
                tree.blocks.span(block),
            );
            builder.declare_parameters(function.signature, arguments, &parameters, outermost);
            let statements = builder.check_statements(None, tree.blocks[block].statements);
            builder.body.blocks[outermost].statements = statements;
        });
        // A part of the signature that failed was reported with the signature,
        // and so was what a call of another function with such a signature
        // leaves here: an invalid expression, with nothing reported again.
        body.has_errors = parameters.contains(&TypeId::INVALID)
            || result == TypeId::INVALID
            || self.module_checks[module].diagnostics[reported..]
                .iter()
                .any(Diagnostic::is_error)
            || (body.exprs.ids()).any(|id| matches!(body.exprs[id].kind, ExprKind::Invalid));
        true
    }
}

impl<'a, 'p> ThirBuilder<'a, 'p> {
    /// Declares the parameters of `signature`, of the function being
    /// checked, in `scope`: each one that is not `const` as a variable of
    /// its type in `types`, and each `const` value as a constant, the value
    /// `arguments` give it or, in the function's own body or signature
    /// (`own_arguments`), one no call gave. A type parameter, `<T>` or
    /// `const T: type`, is a type (`resolve_global`).
    pub(crate) fn declare_parameters(
        &mut self,
        signature: ast::Signature,
        arguments: ConstArguments,
        types: &[TypeId],
        scope: Id<Block>,
    ) {
        let tree = &self.checker.module().ast;
        // Empty, so nothing is allocated, for a function without `const` parameters.
        let given = self.checker.types.arguments(arguments).to_vec();
        // A `<T>` comes first (`copy_parameters`).
        let mut given = given.into_iter().skip(signature.type_parameters.len());
        let mut types = types.iter();
        for parameter in tree.children.lists.items(signature.parameters) {
            let parameter = tree.children.params[parameter];
            let name = parameter.name.unwrap_or_default();
            if !parameter.constant {
                let ty = *types
                    .next()
                    .expect("the signature has each parameter that is not `const`");
                let variable = self.body.add(
                    Variable {
                        ty,
                        mutable: parameter.mutable,
                        initialized: true,
                        scope,
                        hidden: false,
                    },
                    name.span,
                );
                self.declare(name, Local::Variable(variable));
                continue;
            }
            let constant = match given.next() {
                Some(ConstArgument::Value(constant)) => Some(constant),
                _ => None,
            };
            if !matches!(tree.types[parameter.ty], ast::Type::Type) {
                let constant = self.body.constants.push(constant, name.span);
                self.declare(name, Local::Constant(constant));
            }
        }
    }

    /// The THIR of `statements`, in the current block, after `first`, a
    /// statement the checker made for the block. In a copy, the statements
    /// after an `if` its constants decided that always leaves the block are
    /// not checked: they may be wrong for those constants, as `n - 1` past
    /// `if n == 0 { return 0; }` for an unsigned `n` of 0.
    fn check_statements(
        &mut self,
        first: Option<Id<Stmt>>,
        statements: List<ast::Stmt>,
    ) -> List<Stmt> {
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(statements.len() + 1);
        checked.extend(first.map(NonZeroU32::from));
        // A copy's constants may rule out code.
        let in_copy = self.checker.in_copy();
        for written in self.checker.module().ast.children.lists.items(statements) {
            if let Some(statement) = self.check_statement(written) {
                checked.push(statement.into());
                // A decided `if` is checked as the one way it takes; code
                // after a written `return` is checked, as in any function.
                let decided = matches!(
                    self.checker.module().ast.stmts[written],
                    ast::Stmt::If { .. }
                ) && !matches!(self.body.stmts[statement], Stmt::If { .. });
                if in_copy && decided && self.leaves(statement) {
                    break;
                }
            }
        }
        self.body.children.lists.add(&checked)
    }

    /// Whether the checked `statement` always leaves its block: by
    /// `return`, `panic`, `break` or `continue`, or by a block or `if`
    /// whose every way through does.
    fn leaves(&self, statement: Id<Stmt>) -> bool {
        let block_leaves = |block: Id<Block>| {
            let statements = self.body.blocks[block].statements;
            let last = self.body.children.lists.items(statements).last();
            last.is_some_and(|last| self.leaves(last))
        };
        match self.body.stmts[statement] {
            Stmt::Return(_) | Stmt::Panic(_) | Stmt::Break | Stmt::Continue => true,
            Stmt::Block(block) => block_leaves(block),
            Stmt::If {
                then,
                otherwise: Some(otherwise),
                ..
            } => block_leaves(then) && self.leaves(otherwise),
            _ => false,
        }
    }

    /// The THIR of `block`; the names it declares end with it. It is added
    /// before the blocks inside it.
    fn check_block(&mut self, block: Id<ast::Block>) -> Id<Block> {
        let enclosing = self.open_block(block);
        self.close_block(block, enclosing, None)
    }

    /// A block inside the current one, made for the code at `span`, of the
    /// statements `build` gives, which declare no name; the hidden
    /// variables `build` makes end with it. It is added before the blocks
    /// `build` adds.
    pub(crate) fn add_block(
        &mut self,
        span: Span,
        build: impl FnOnce(&mut Self) -> Vec<Id<Stmt>>,
    ) -> Id<Block> {
        let block = Block {
            parent: Some(self.block),
            statements: List::EMPTY,
        };
        let block = self.body.add(block, span);
        let enclosing = std::mem::replace(&mut self.block, block);
        let statements: Vec<NonZeroU32> = build(self).into_iter().map(Id::into).collect();
        self.block = enclosing;
        self.body.blocks[block].statements = self.body.children.lists.add(&statements);
        block
    }

    /// Makes `block` the current block, for names declared before its
    /// statements; gives the enclosing block and where its names start, for
    /// `close_block`.
    fn open_block(&mut self, block: Id<ast::Block>) -> (Id<Block>, usize) {
        let opened = Block {
            parent: Some(self.block),
            statements: List::EMPTY,
        };
        let opened = self
            .body
            .add(opened, self.checker.module().ast.blocks.span(block));
        let start = std::mem::replace(&mut self.block_start, self.names.len());
        (std::mem::replace(&mut self.block, opened), start)
    }

    /// Checks the statements of `block`, opened by `open_block`, after
    /// `first` (`check_statements`), and makes `enclosing` the current block
    /// again.
    fn close_block(
        &mut self,
        block: Id<ast::Block>,
        enclosing: (Id<Block>, usize),
        first: Option<Id<Stmt>>,
    ) -> Id<Block> {
        let statements =
            self.check_statements(first, self.checker.module().ast.blocks[block].statements);
        let closed = self.block;
        for declared in self.names.drain(self.block_start..).rev() {
            match declared.hides {
                Some(outer) => self.innermost.insert(declared.name, outer),
                None => self.innermost.remove(declared.name),
            };
        }
        (self.block, self.block_start) = enclosing;
        self.body.blocks[closed].statements = statements;
        closed
    }

    /// A variable of the current block with no name, made for the code at
    /// `span`.
    pub(crate) fn add_hidden_variable(
        &mut self,
        ty: TypeId,
        mutable: bool,
        span: Span,
    ) -> Id<Variable> {
        let variable = Variable {
            ty,
            mutable,
            initialized: true,
            scope: self.block,
            hidden: true,
        };
        self.body.add(variable, span)
    }

    /// Declares `name` in the current block. It may
    /// shadow a name of an outer block until the block ends. A name the block
    /// already declares is reported and keeps its first meaning; the
    /// parameters are in the body's outermost block, as in Go. A type's name,
    /// built-in or declared, is reported and stays the type's: a value never
    /// hides a type.
    pub(crate) fn declare(&mut self, name: Ident, local: Local) {
        let text = name.text(&self.checker.module().ast.file);
        if text.is_empty() {
            return; // missing after a syntax error
        }
        if let Some(&position) = self.innermost.get(text)
            && position >= self.block_start
        {
            let first = self.body.declared_at(self.names[position].local);
            let diagnostic = report_redeclared(
                &self.checker.module().ast.file,
                text,
                "block",
                name.span,
                first,
            );
            self.checker.report(diagnostic);
            return;
        }
        // No local holds a type's name, so only top-level names can clash.
        let ty = match self.checker.resolve_global(text) {
            Some(Symbol::Builtin(_) | Symbol::TypeArgument { .. }) => None,
            Some(Symbol::Type(definition)) => Some(definition),
            _ => {
                self.checker
                    .record(name.span, FactKind::Name(Symbol::Local(local)));
                let hides = self.innermost.insert(text, self.names.len());
                self.names.push(Declared {
                    name: text,
                    local,
                    hides,
                });
                return;
            }
        };
        let file = &self.checker.module().ast.file;
        let message = format!("`{text}` is a type; a value can't take its name");
        let mut diagnostic = Diagnostic::new(diagnostics::ALREADY_DECLARED, message).label(
            file,
            name.span,
            "declared here",
        );
        if let Some(definition) = ty {
            let (declared, declared_in) =
                model::program::declared_name(self.checker.modules, definition);
            diagnostic = diagnostic.label(declared_in, declared.span, "the type is declared here");
        }
        self.checker.report(diagnostic);
    }

    /// What the name written at `span` stands for: a name of the current
    /// block or an outer one, else a top-level name (`Checker::resolve_global`),
    /// recorded (`Checker::record`). `None` after reporting that nothing
    /// declares it.
    pub(crate) fn resolve(&mut self, span: Span) -> Option<Symbol> {
        let name = self.checker.module().ast.file.slice(span);
        let symbol = if let Some(&position) = self.innermost.get(name) {
            Symbol::Local(self.names[position].local)
        } else {
            let Some(symbol) = self.checker.resolve_global(name) else {
                let locals = self.names.iter().map(|declared| declared.name);
                self.checker.report_unknown_name(name, span, locals);
                return None;
            };
            symbol
        };
        self.checker.record(span, FactKind::Name(symbol));
        Some(symbol)
    }

    /// The THIR of `statement`; `None` for a `const`, which only declares a
    /// name, and for a statement with an error.
    #[deny(
        clippy::wildcard_enum_match_arm,
        clippy::match_wildcard_for_single_variants
    )]
    fn check_statement(&mut self, statement: Id<ast::Stmt>) -> Option<Id<Stmt>> {
        // Blocks in blocks and long `else if` chains are a deep recursion.
        source::grow_stack(|| {
            let tree = &self.checker.module().ast;
            let span = tree.stmts.span(statement);
            let checked = match tree.stmts[statement] {
                ast::Stmt::Block(block) => Stmt::Block(self.check_block(block)),
                ast::Stmt::Let {
                    mutable,
                    name,
                    ty,
                    value,
                } => self.check_let_statement(mutable, name, ty, value),
                ast::Stmt::Const(declaration) => {
                    let value = self.check_const_statement(declaration);
                    let ast::Decl::Const { name, .. } = tree.decls[declaration] else {
                        unreachable!("a const statement holds a const declaration")
                    };
                    let constant = self.body.constants.push(value, name.span);
                    self.declare(name, Local::Constant(constant));
                    return None;
                }
                ast::Stmt::Return(value) => Stmt::Return(self.check_return_value(span, value)),
                ast::Stmt::Panic(message) => self.check_panic(message)?,
                ast::Stmt::Match { .. } => crate::enums::check_match(self, span)?,
                ast::Stmt::Expression(value) => Stmt::Expression(self.add_expr(value, None)),
                ast::Stmt::Assign { target, value } => self.assign(target, value)?,
                ast::Stmt::If {
                    condition,
                    then,
                    otherwise,
                } => {
                    let condition = self.add_required(condition, TypeId::BOOL);
                    // A copy holds only the branch its constants choose.
                    let chosen = if let ExprKind::Constant(Value::Bool(taken)) =
                        self.body.exprs[condition].kind
                        && self.checker.in_copy()
                    {
                        Some(taken)
                    } else {
                        None
                    };
                    match chosen {
                        Some(true) => Stmt::Block(self.check_block(then?)),
                        Some(false) => {
                            return otherwise.and_then(|otherwise| self.check_statement(otherwise));
                        }
                        None => {
                            let then = self.check_block(then?); // missing after a syntax error
                            let otherwise =
                                otherwise.and_then(|otherwise| self.check_statement(otherwise));
                            Stmt::If {
                                condition,
                                then,
                                otherwise,
                            }
                        }
                    }
                }
                ast::Stmt::Destructure { pattern, .. } => {
                    self.checker.not_supported(span, "destructuring `let`s");
                    // Its names are still declared, so their uses report nothing
                    // more.
                    let ast::Pattern::Struct(fields) = tree.patterns[pattern] else {
                        return None; // the parser reported it
                    };
                    for field in tree.children.lists.items(fields) {
                        if let ast::Pattern::Field { field, binding } = tree.patterns[field] {
                            let name = binding.unwrap_or(field);
                            let unknown = Variable {
                                ty: TypeId::INVALID,
                                mutable: true,
                                initialized: true,
                                scope: self.block,
                                hidden: false,
                            };
                            let variable = self.body.add(unknown, name.span);
                            self.declare(name, Local::Variable(variable));
                        }
                    }
                    return None;
                }
                ast::Stmt::Loop { control, body } => 'checked: {
                    let mut condition = None;
                    if let Some(control) = control {
                        let checked = self.check_expr(control, None);
                        let control_span = tree.exprs.span(control);
                        if let Type::Range { .. } = self.checker.types[checked.ty] {
                            let range = self.body.add(checked, control_span);
                            break 'checked self.check_for_range(None, None, range, body?);
                        }
                        let checked = self.require(checked, control, TypeId::BOOL);
                        condition = Some(self.body.add(checked, control_span));
                    }
                    let body = body.map(|body| {
                        let enclosing = self.open_block(body);
                        self.check_loop_body(body, enclosing, None)
                    });
                    Stmt::Loop {
                        condition,
                        body: body?, // missing after a syntax error
                    }
                }
                ast::Stmt::ForIn {
                    index,
                    value,
                    iterable,
                    body,
                } => {
                    let iterable = self.add_expr(iterable, None);
                    let types = &self.checker.types;
                    let ty = self.body.exprs[iterable].ty;
                    let walked = if let Type::Reference { target, .. } = types[ty] {
                        target
                    } else {
                        ty
                    };
                    // What walking a type each copy decides means, each copy
                    // decides; the own check, never lowered, checks the body
                    // as a range's.
                    if matches!(types[ty], Type::Range { .. })
                        || types[walked].is_decided_per_copy()
                    {
                        self.check_for_range(index, Some(value), iterable, body?)
                    } else {
                        self.check_for_array(index, value, iterable, body?)?
                    }
                }
                ast::Stmt::Break | ast::Stmt::Continue if self.loops == 0 => {
                    let jump = if let ast::Stmt::Break = tree.stmts[statement] {
                        "break"
                    } else {
                        "continue"
                    };
                    let message = format!("`{jump}` outside a loop");
                    self.checker
                        .error(diagnostics::INVALID_STATEMENT, message, span);
                    return None;
                }
                ast::Stmt::Break => Stmt::Break,
                ast::Stmt::Continue => Stmt::Continue,
            };
            Some(self.body.add(checked, span))
        })
    }

    /// `panic message;`: the message is a plain string literal.
    fn check_panic(&mut self, message: Id<ast::Expr>) -> Option<Stmt> {
        let tree = &self.checker.module().ast;
        let message = tree.unparenthesized(message);
        let span = tree.exprs.span(message);
        // shortcut: a plain string literal only, as there are no run-time
        // strings; take a string value once there are.
        if !matches!(
            tree.exprs[message],
            ast::Expr::String(ast::StringKind::Plain)
        ) {
            let checked = self.add_expr(message, None);
            if self.body.exprs[checked].ty != TypeId::INVALID {
                let what = "`panic` messages other than string literals";
                self.checker.not_supported(span, what);
            }
            return None;
        }
        let message = self.string_literal(span, TypeId::STR);
        Some(Stmt::Panic(self.body.add(message, span)))
    }
}

#[cfg(test)]
mod tests;
