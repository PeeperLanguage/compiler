//! Checking a function body into THIR: its statements, and the names its
//! blocks declare.

mod assignments;
mod declarations;
mod loops;

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::DefId;
use model::ast;
use model::ast::Ident;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Block;
use model::thir::Body;
use model::thir::FactKind;
use model::thir::Local;
use model::thir::Stmt;
use model::thir::Symbol;
use model::thir::Variable;
use source::Span;

use crate::Checker;
use crate::Checking;
use crate::builder::ThirBuilder;
use crate::expression::TypeExpectation;
use crate::function::function_declaration;
use crate::scope::report_redeclared;

impl Checker<'_> {
    /// Puts in `body` the THIR of the body of the function `definition`,
    /// asked for at `used_at` and built only to run at compile time when
    /// `to_run` (`Checking`), with `has_errors` set when checking it reported
    /// an error; what `body` held is dropped, its memory kept. False, with
    /// `body` untouched, for a function without a body or whose signature had
    /// an error.
    pub fn check_body(
        &mut self,
        definition: DefId,
        used_at: Span,
        body: &mut Body,
        to_run: bool,
    ) -> bool {
        let function = function_declaration(self.modules, definition);
        let Some(block) = function.body else {
            return false;
        };
        let ty = self.function_type(definition, used_at);
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
        self.checking.push(Checking {
            definition,
            used_at,
            to_run,
        });
        body.clear();
        let mut builder = ThirBuilder::new(self, body, result);
        let outermost = Block {
            parent: None,
            statements: List::EMPTY,
        };
        let outermost = builder.body.add(outermost, tree.blocks.span(block));
        for (parameter, &ty) in tree
            .children
            .lists
            .items(function.signature.parameters)
            .zip(&parameters)
        {
            let parameter = tree.children.params[parameter];
            let name = parameter.name.unwrap_or_default();
            let variable = builder.body.add(
                Variable {
                    ty,
                    mutable: parameter.mutable,
                    initialized: true,
                    scope: outermost,
                    hidden: false,
                },
                name.span,
            );
            builder.declare(name, Local::Variable(variable));
        }
        let statements = builder.check_statements(None, tree.blocks[block].statements);
        builder.body.blocks[outermost].statements = statements;
        // A part of the signature that failed was reported with the signature.
        self.checking.pop();
        body.has_errors = parameters.contains(&TypeId::INVALID)
            || result == TypeId::INVALID
            || self.module_checks[module].diagnostics[reported..]
                .iter()
                .any(Diagnostic::is_error);
        true
    }
}

impl<'a, 'p> ThirBuilder<'a, 'p> {
    /// The THIR of `statements`, in the current block, after `first`, a
    /// statement the checker made for the block.
    fn check_statements(
        &mut self,
        first: Option<Id<Stmt>>,
        statements: List<ast::Stmt>,
    ) -> List<Stmt> {
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(statements.len() + 1);
        checked.extend(first.map(NonZeroU32::from));
        for statement in self.checker.module().ast.children.lists.items(statements) {
            if let Some(statement) = self.check_statement(statement) {
                checked.push(statement.into());
            }
        }
        self.body.children.lists.add(&checked)
    }

    /// The THIR of `block`; the names it declares end with it. It is added
    /// before the blocks inside it.
    fn check_block(&mut self, block: Id<ast::Block>) -> Id<Block> {
        let enclosing = self.open_block(block);
        self.close_block(block, enclosing, None)
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
        self.names.truncate(self.block_start);
        (self.block, self.block_start) = enclosing;
        self.body.blocks[closed].statements = statements;
        closed
    }

    /// A variable of the current block with no name, made for the code at
    /// `span`.
    fn add_hidden_variable(&mut self, ty: TypeId, span: Span) -> Id<Variable> {
        let variable = Variable {
            ty,
            mutable: false,
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
    fn declare(&mut self, name: Ident, local: Local) {
        let text = name.text(&self.checker.module().ast.file);
        if text.is_empty() {
            return; // missing after a syntax error
        }
        let block = &self.names[self.block_start..];
        if let Some(&(_, first)) = block.iter().find(|&&(declared, _)| declared == text) {
            let first = self.body.declared(first);
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
        let ty = match self.checker.global(text) {
            Some(Symbol::Builtin(_)) => None,
            Some(Symbol::Type(definition)) => Some(definition),
            _ => {
                self.checker
                    .record(name.span, FactKind::Name(Symbol::Local(local)));
                self.names.push((text, local));
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
            let (declared, declared_in) = project::declared_name(self.checker.modules, definition);
            diagnostic = diagnostic.label(declared_in, declared.span, "the type is declared here");
        }
        self.checker.report(diagnostic);
    }

    /// What the name written at `span` stands for: a name of the current
    /// block or an outer one, else a top-level name (`Checker::global`),
    /// recorded (`Checker::record`). `None` after reporting that nothing
    /// declares it.
    pub(crate) fn resolve(&mut self, span: Span) -> Option<Symbol> {
        let name = self.checker.module().ast.file.slice(span);
        let found = self
            .names
            .iter()
            .rev()
            .find(|&&(declared, _)| declared == name);
        let symbol = if let Some(&(_, local)) = found {
            Symbol::Local(local)
        } else {
            let Some(symbol) = self.checker.global(name) else {
                self.report_undeclared(name, span);
                return None;
            };
            symbol
        };
        self.checker.record(span, FactKind::Name(symbol));
        Some(symbol)
    }

    /// The THIR of `statement`; `None` for a `const`, which only declares a
    /// name, and for a statement with an error.
    fn check_statement(&mut self, statement: Id<ast::Stmt>) -> Option<Id<Stmt>> {
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
            ast::Stmt::Panic(message) => {
                let message = tree.unparenthesized(message);
                let span = tree.exprs.span(message);
                // shortcut: a plain string literal only, as there are no
                // run-time strings; take a string value once there are.
                if !matches!(tree.exprs[message], ast::Expr::String { c_string: false }) {
                    let checked = self.add_expr(message, TypeExpectation::Hint(None));
                    if self.body.exprs[checked].ty != TypeId::INVALID {
                        let what = "`panic` messages other than string literals";
                        self.checker.not_supported(span, what);
                    }
                    return None;
                }
                let message = self.c_string(span);
                Stmt::Panic(self.body.add(message, span))
            }
            ast::Stmt::Expression(value) => {
                Stmt::Expression(self.add_expr(value, TypeExpectation::Hint(None)))
            }
            ast::Stmt::Assign { target, value } => self.assign(target, value)?,
            ast::Stmt::If {
                condition,
                then,
                otherwise,
            } => {
                let condition = self.add_expr(condition, TypeExpectation::Required(TypeId::BOOL));
                let then = self.check_block(then?); // missing after a syntax error
                let otherwise = otherwise.and_then(|otherwise| self.check_statement(otherwise));
                Stmt::If {
                    condition,
                    then,
                    otherwise,
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
                    let checked = self.check_expr(control, TypeExpectation::Hint(None));
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
                let iterable = self.add_expr(iterable, TypeExpectation::Hint(None));
                let ty = self.body.exprs[iterable].ty;
                match self.checker.types[ty] {
                    Type::Range { .. } => self.check_for_range(index, Some(value), iterable, body?),
                    _ => self.check_for_array(index, value, iterable, body?)?,
                }
            }
            ast::Stmt::Break | ast::Stmt::Continue if self.loops == 0 => {
                let jump = match tree.stmts[statement] {
                    ast::Stmt::Break => "break",
                    _ => "continue",
                };
                let message = format!("`{jump}` outside a loop");
                self.checker
                    .error(diagnostics::INVALID_STATEMENT, message, span);
                return None;
            }
            ast::Stmt::Break => Stmt::Break,
            ast::Stmt::Continue => Stmt::Continue,
            ast::Stmt::Match { .. } => {
                self.checker.not_supported(span, "`match` statements");
                return None;
            }
        };
        Some(self.body.add(checked, span))
    }
}

#[cfg(test)]
mod tests;
