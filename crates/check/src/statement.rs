//! Checking a function body into THIR: its statements, and the names its
//! blocks declare.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use bigint::Limbs;
use diagnostics::Diagnostic;
use model::DefId;
use model::ast;
use model::ast::Ident;
use model::constant::Constant;
use model::constant::Value;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Block;
use model::thir::Body;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::FactKind;
use model::thir::Local;
use model::thir::Stmt;
use model::thir::Symbol;
use model::thir::Variable;
use source::Span;

use crate::Checker;
use crate::Checking;
use crate::expression::TypeExpectation;
use crate::function::function_declaration;
use crate::scope::report_redeclared;

/// Building the THIR of one piece of code: a function's body, or a
/// constant's value. It checks the code as it builds it.
pub(crate) struct ThirBuilder<'a, 'p> {
    /// The checker, borrowed for the build: its module, tables and
    /// diagnostics.
    pub(crate) checker: &'a mut Checker<'p>,
    pub(crate) body: &'a mut Body,
    /// The names declared in the enclosing blocks so far, innermost last. Few
    /// names are in scope at once, so a search from the end beats hashing.
    pub(crate) names: Vec<(&'p str, Local)>,
    /// Where in `names` the current block's names start.
    block_start: usize,
    /// The current block, for the variables it declares.
    block: Id<Block>,
    result: TypeId, // the function's result type
    loops: u32,     // how many loops the code being checked is in
    /// Whether the code being checked is a constant's value, whose calls run
    /// at compile time (`Checker::evaluate_call`).
    pub(crate) in_constant: bool,
}

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
            .lists
            .items(function.signature.parameters)
            .zip(&parameters)
        {
            let parameter = tree.params[parameter];
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
    /// Building code into `body` whose `return` gives `result`.
    pub(crate) fn new(
        checker: &'a mut Checker<'p>,
        body: &'a mut Body,
        result: TypeId,
    ) -> ThirBuilder<'a, 'p> {
        ThirBuilder {
            checker,
            body,
            names: Vec::new(),
            block_start: 0,
            block: Id::from_index(0), // the function's, added first (`Checker::check_body`)
            result,
            loops: 0,
            in_constant: false,
        }
    }

    /// The THIR of `statements`, in the current block, after `first`, a
    /// statement the checker made for the block.
    fn check_statements(
        &mut self,
        first: Option<Id<Stmt>>,
        statements: List<ast::Stmt>,
    ) -> List<Stmt> {
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(statements.len() + 1);
        checked.extend(first.map(NonZeroU32::from));
        for statement in self.checker.module().ast.lists.items(statements) {
            if let Some(statement) = self.check_statement(statement) {
                checked.push(statement.into());
            }
        }
        self.body.lists.add(&checked)
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

    /// `close_block` for a loop's `body`, in which `break` and `continue`
    /// are allowed.
    fn check_loop_body(
        &mut self,
        body: Id<ast::Block>,
        enclosing: (Id<Block>, usize),
        first: Option<Id<Stmt>>,
    ) -> Id<Block> {
        self.loops += 1;
        let body = self.close_block(body, enclosing, first);
        self.loops -= 1;
        body
    }

    /// A loop's variable `name`, set each pass, declared in the current
    /// block.
    fn declare_loop_variable(&mut self, ty: TypeId, name: Ident) -> Id<Variable> {
        let variable = Variable {
            ty,
            mutable: false,
            initialized: true,
            scope: self.block,
            hidden: false,
        };
        let variable = self.body.add(variable, name.span);
        self.declare(name, Local::Variable(variable));
        variable
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
                for field in tree.lists.items(fields) {
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
            ast::Stmt::Loop { condition, body } => {
                let condition = condition.map(|condition| {
                    self.add_expr(condition, TypeExpectation::Required(TypeId::BOOL))
                });
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
                match (value, &self.checker.types[ty]) {
                    // The parser leaves out only a range loop's value.
                    (None, _) | (_, Type::Range { .. }) => {
                        self.check_for_range(index, value, iterable, body?)
                    }
                    (Some(value), _) => self.check_for_array(index, value, iterable, body?)?,
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

    /// `let mut name: ty = value;`. The variable is declared after its value is
    /// checked, so the value cannot use it.
    fn check_let_statement(
        &mut self,
        mutable: bool,
        name: Ident,
        ty: Option<Id<ast::Type>>,
        value: Option<Id<ast::Expr>>,
    ) -> Stmt {
        let declared = ty.map(|ty| self.checker.resolve_value_type(ty));
        let initialized = value.is_some();
        let (ty, value) = match (declared, value) {
            (Some(ty), value) => (
                ty,
                value.map(|value| self.add_expr(value, TypeExpectation::Required(ty))),
            ),
            (None, Some(value)) => {
                let value = self.add_expr(value, TypeExpectation::Hint(None));
                let ty = self.body.exprs[value].ty;
                if ty == TypeId::VOID {
                    let span = self.body.exprs.span(value);
                    let message = "this expression has no value to bind";
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, span);
                    (TypeId::INVALID, None)
                } else {
                    (ty, Some(value))
                }
            }
            (None, None) => {
                let text = name.text(&self.checker.module().ast.file);
                let message = format!("`{text}` needs a type or a value");
                self.checker
                    .error(diagnostics::MISSING_TYPE, message, name.span);
                (TypeId::INVALID, None)
            }
        };
        let variable = Variable {
            ty,
            mutable,
            initialized,
            scope: self.block,
            hidden: false,
        };
        let text = name.text(&self.checker.module().ast.file);
        let shadowed = self
            .names
            .iter()
            .rposition(|&(declared, ..)| declared == text);
        if let Some(position) = shadowed
            && position < self.block_start
            && let Local::Variable(outer) = self.names[position].1
            && self.body.variables[outer].mutable
        {
            // Likely meant as `x = ...`: the outer local stays unchanged.
            let file = &self.checker.module().ast.file;
            self.checker.report(
                Diagnostic::new(
                    diagnostics::HIDDEN_MUTABLE,
                    format!("this `let` hides the outer `mut {text}`"),
                )
                .label(
                    file,
                    name.span,
                    format!("a new `{text}`, only inside this block"),
                )
                .label(
                    file,
                    self.body.variables.span(outer),
                    format!("the outer `{text}`, declared `mut` here"),
                )
                .help(
                    format!("to change the outer `{text}`, write `{text} = ...` without `let`"),
                    Vec::new(),
                ),
            );
        }
        let variable = self.body.add(variable, name.span);
        self.declare(name, Local::Variable(variable));
        Stmt::Let { variable, value }
    }

    /// `for value in range { body }` or `for index, value in range`, or
    /// `for start..end { body }` without a `value`, over the checked
    /// `range`, a `range[T]` (invalid after an error): `value` is a `T`.
    fn check_for_range(
        &mut self,
        index: Option<Ident>,
        value: Option<Ident>,
        range: Id<Expr>,
        body: Id<ast::Block>,
    ) -> Stmt {
        let element = match self.checker.types[self.body.exprs[range].ty] {
            Type::Range { element, .. } => element,
            _ => TypeId::INVALID,
        };
        let enclosing = self.open_block(body);
        let index = index.map(|index| {
            let usize = self.checker.usize_type();
            self.declare_loop_variable(usize, index)
        });
        let value = value.map(|value| self.declare_loop_variable(element, value));
        let body = self.check_loop_body(body, enclosing, None);
        Stmt::ForRange {
            index,
            value,
            range,
            body,
        }
    }

    /// The step of a range, written `step` or else 1 at `range_span`, of
    /// the range type's step type `step_type` (`Types::range`), and not a
    /// constant zero. With an invalid `step_type`, after an error in the
    /// ends, it is checked only for its own errors.
    pub(crate) fn check_range_step(
        &mut self,
        step: Option<Id<ast::Expr>>,
        step_type: TypeId,
        range_span: Span,
    ) -> Id<Expr> {
        let Some(step) = step else {
            let one = match self.checker.types[step_type] {
                Type::Integer(unsigned) => Expr {
                    ty: step_type,
                    kind: ExprKind::Constant(Value::Integer(Limbs::new(unsigned.bits, &[1]))),
                },
                _ => Expr::INVALID,
            };
            return self.body.add(one, range_span);
        };
        let expectation = match step_type {
            TypeId::INVALID => TypeExpectation::Hint(None),
            ty => TypeExpectation::Required(ty),
        };
        let step = self.add_expr(step, expectation);
        if let ExprKind::Constant(Value::Integer(ref limbs)) = self.body.exprs[step].kind
            && limbs.as_slice().iter().all(|&limb| limb == 0)
        {
            let span = self.body.exprs.span(step);
            let message = "a range's step must not be zero";
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, span);
        }
        step
    }

    /// `for value in items { body }` or `for index, value in items`, over an
    /// array or a reference to one, computed once before the first pass.
    /// Written as the code that runs it, a block of
    ///
    /// ```text
    /// let items = <iterable>; // unnamed; ends after the loop
    /// for index in 0..N { let value = items[index]; body }
    /// ```
    ///
    /// where `value` is a copy of the element, which must copy, or
    /// `&items.*[index]` or `&mut items.*[index]` when `items` is a `&` or
    /// `&mut` reference, which keeps the array borrowed for the whole loop.
    /// Without `index`, the counter is unnamed.
    fn check_for_array(
        &mut self,
        index: Option<Ident>,
        value: Ident,
        iterable: Id<Expr>,
        body: Id<ast::Block>,
    ) -> Option<Stmt> {
        self.lend_variable_again(iterable);
        let (items_type, span) = (self.body.exprs[iterable].ty, self.body.exprs.span(iterable));
        let types = &self.checker.types;
        let (array, borrow) = match types[items_type] {
            Type::Reference { target, mutable } => (target, Some(mutable)),
            _ => (items_type, None),
        };
        // After an error, the body is still checked, with an invalid `value`.
        let (element, length) = if let Type::Array { element, length } = types[array] {
            (element, length)
        } else {
            if items_type != TypeId::INVALID {
                self.checker
                    .not_supported(span, "`for` over values other than ranges and arrays");
            }
            (TypeId::INVALID, 0)
        };
        if borrow.is_none() && !self.checker.types.is_copy(element) {
            let file = &self.checker.module().ast.file;
            let array_text = self.checker.type_text(array);
            let element_text = self.checker.type_text(element);
            let diagnostic = Diagnostic::new(
                diagnostics::PARTIAL_MOVE,
                "cannot move an element out of its array",
            )
            .label(
                file,
                span,
                format!(
                    "`for` takes each element of this `{array_text}`, and `{element_text}` moves"
                ),
            )
            .help(
                format!(
                    "loop over `&{}` for a reference to each element",
                    file.slice(span)
                ),
                Vec::new(),
            );
            self.checker.report(diagnostic);
        }
        // The unnamed `items`, in a block of its own that ends after the loop.
        let body_span = self.checker.module().ast.blocks.span(body);
        let whole = Span::new(span.start as usize, body_span.end as usize);
        let holder = Block {
            parent: Some(self.block),
            statements: List::EMPTY,
        };
        let holder = self.body.add(holder, whole);
        let outer = std::mem::replace(&mut self.block, holder);
        let items = self.add_hidden_variable(items_type, span);
        let let_items = Stmt::Let {
            variable: items,
            value: Some(iterable),
        };
        let let_items = self.body.add(let_items, span);
        // `for index in 0..N`, each pass first setting `value`.
        let usize = self.checker.usize_type();
        let Type::Integer(integer) = self.checker.types[usize] else {
            unreachable!("`usize` is an integer")
        };
        let mut constant = |number: u64| {
            let value = Value::Integer(Limbs::new(integer.bits, &[number]));
            let constant = Expr {
                ty: usize,
                kind: ExprKind::Constant(value),
            };
            self.body.add(constant, span)
        };
        let range = Expr {
            ty: (self.checker.types.range(usize)).expect("`usize` is an integer"),
            kind: ExprKind::Range {
                start: constant(0),
                end: constant(length),
                step: constant(1), // `usize` is its own step type
                inclusive: false,
            },
        };
        let range = self.body.add(range, span);
        let enclosing = self.open_block(body);
        let counter = match index {
            Some(index) => self.declare_loop_variable(usize, index),
            None => self.add_hidden_variable(usize, span),
        };
        let (element_value, value_type) =
            self.array_element(items, counter, array, element, borrow, span);
        let variable = self.declare_loop_variable(value_type, value);
        let let_value = Stmt::Let {
            variable,
            value: Some(element_value),
        };
        let let_value = self.body.add(let_value, value.span);
        let body = self.check_loop_body(body, enclosing, Some(let_value));
        let range = Stmt::ForRange {
            index: None,
            value: Some(counter),
            range,
            body,
        };
        let range = self.body.add(range, whole);
        self.block = outer;
        let statements: [NonZeroU32; 2] = [let_items.into(), range.into()];
        self.body.blocks[holder].statements = self.body.lists.add(&statements);
        Some(Stmt::Block(holder))
    }

    /// The value `check_for_array` gives `value` each pass, and its type:
    /// `items[counter]`, or `&items.*[counter]` or `&mut items.*[counter]`
    /// when `borrow` says `items` is a `&` or `&mut` reference to an `array`
    /// of `element`.
    fn array_element(
        &mut self,
        items: Id<Variable>,
        counter: Id<Variable>,
        array: TypeId,
        element: TypeId,
        borrow: Option<bool>,
        span: Span,
    ) -> (Id<Expr>, TypeId) {
        let read = Expr {
            ty: self.body.variables[items].ty,
            kind: ExprKind::Variable(items),
        };
        let mut place = self.body.add(read, span);
        if borrow.is_some() {
            let referred = Expr {
                ty: array,
                kind: ExprKind::Dereference(place),
            };
            place = self.body.add(referred, span);
        }
        let at = Expr {
            ty: self.body.variables[counter].ty,
            kind: ExprKind::Variable(counter),
        };
        let at = self.body.add(at, span);
        let read = Expr {
            ty: element,
            kind: ExprKind::Index {
                array: place,
                index: at,
            },
        };
        let read = self.body.add(read, span);
        let Some(mutable) = borrow else {
            return (read, element);
        };
        let ty = self.checker.reference_type(mutable, element, span);
        let reference = Expr {
            ty,
            kind: ExprKind::Borrow(read),
        };
        (self.body.add(reference, span), ty)
    }

    /// `const name: ty = value;`: a name for a value known at compile time.
    /// Without a type, the constant takes its value's type. Gives its type and
    /// value, or `None` after an error.
    pub(crate) fn check_const_statement(&mut self, declaration: Id<ast::Decl>) -> Option<Constant> {
        let ast::Decl::Const {
            name, ty, value, ..
        } = self.checker.module().ast.decls[declaration]
        else {
            unreachable!("`{declaration:?}` is a const declaration")
        };
        let declared = ty.map(|ty| self.checker.resolve_value_type(ty));
        let Some(value) = value else {
            let text = name.text(&self.checker.module().ast.file);
            let message = format!("constant `{text}` needs a value");
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, name.span);
            return None;
        };
        let span = self.checker.module().ast.exprs.span(value);
        let outer = std::mem::replace(&mut self.in_constant, true);
        let checked = match declared {
            Some(ty) => self.check_expr(value, TypeExpectation::Required(ty)),
            None => self.check_expr(value, TypeExpectation::Hint(None)),
        };
        self.in_constant = outer;
        match checked.kind {
            ExprKind::Constant(value) => Some(Constant {
                ty: checked.ty,
                value,
            }),
            ExprKind::Invalid => None,
            _ => {
                let message = "the value of a constant must be known at compile time";
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, span);
                None
            }
        }
    }

    /// The value of `return value;`, checked against the function's result.
    fn check_return_value(&mut self, span: Span, value: Option<Id<ast::Expr>>) -> Option<Id<Expr>> {
        match value {
            Some(value) if self.result == TypeId::VOID => {
                let message = "this function has no result, so `return` takes no value";
                self.checker.error(
                    diagnostics::INVALID_RETURN,
                    message,
                    self.checker.module().ast.exprs.span(value),
                );
                self.add_expr(value, TypeExpectation::Hint(None)); // for its own errors
                None
            }
            Some(value) => {
                let value = self.add_expr(value, TypeExpectation::Required(self.result));
                // `@place` of the function's own storage, gone once it returns.
                // shortcut: only a written `return @x`; an address kept in a
                // variable or a field first (`let p = @x; return p;`) is
                // missed. Track where addresses flow in MIR, as loans do,
                // when that is decided.
                if let ExprKind::Address(place) = self.body.exprs[value].kind
                    && let Some((variable, None)) = self.place_root(place)
                {
                    let file = &self.checker.module().ast.file;
                    let declared = self.body.variables.span(variable);
                    let name = file.slice(declared);
                    self.checker.report(
                        Diagnostic::new(
                            diagnostics::LOCAL_ADDRESS_RETURNED,
                            "cannot return pointer to local storage",
                        )
                        .label(file, self.body.exprs.span(value), "")
                        .label(
                            file,
                            declared,
                            format!("`{name}` is gone once the function returns"),
                        ),
                    );
                }
                Some(value)
            }
            None => {
                if self.result != TypeId::VOID && self.result != TypeId::INVALID {
                    let result = self.checker.type_text(self.result);
                    let message = format!("`return` needs a value of type `{result}`");
                    self.checker
                        .error(diagnostics::INVALID_RETURN, message, span);
                }
                None
            }
        }
    }

    /// `target = value;`, where the target is a mutable variable, a field of
    /// one, or what a `&mut` reference refers to.
    fn assign(&mut self, target: Id<ast::Expr>, value: Id<ast::Expr>) -> Option<Stmt> {
        let Some(target) = self.assigned_place(target) else {
            self.add_expr(value, TypeExpectation::Hint(None)); // for its own errors
            return None;
        };
        let value = self.add_expr(value, TypeExpectation::Required(self.body.exprs[target].ty));
        Some(Stmt::Assign { target, value })
    }

    /// The variable, field, element or referred value that `target` names,
    /// reporting one that cannot be set (`report_unwritable`). One that cannot
    /// be set is still given, so the value is checked against its type.
    fn assigned_place(&mut self, target: Id<ast::Expr>) -> Option<Id<Expr>> {
        let tree = &self.checker.module().ast;
        let place = match tree.exprs[target] {
            ast::Expr::Selector { .. } | ast::Expr::Dereference(_) | ast::Expr::Index { .. } => {
                self.add_expr(target, TypeExpectation::Hint(None))
            }
            _ => {
                let variable = self.assigned_variable(target)?;
                let Variable {
                    ty, initialized, ..
                } = self.body.variables[variable];
                let kind = ExprKind::Variable(variable);
                let place = self.body.add(Expr { ty, kind }, tree.exprs.span(target));
                // Declared without a value, it is set once later; the checks
                // on MIR report one set twice.
                if !initialized {
                    return Some(place);
                }
                place
            }
        };
        if let ExprKind::Invalid = self.body.exprs[place].kind {
            return None; // its error was reported
        }
        let Some((variable, reference)) = self.place_root(place) else {
            let message = match self.body.exprs[place].kind {
                // A field read that is no place: an array's `len` (`check_field`).
                ExprKind::Constant(_) | ExprKind::Length(_) => "an array's `len` cannot be set",
                _ => "only a variable's fields can be set",
            };
            let span = self.body.exprs.span(place);
            self.checker
                .error(diagnostics::INVALID_ASSIGNMENT, message, span);
            return None;
        };
        self.report_unwritable(place, variable, reference, "assign to");
        Some(place)
    }

    /// The variable that `target` names, reporting a target that is not a
    /// variable.
    fn assigned_variable(&mut self, target: Id<ast::Expr>) -> Option<Id<Variable>> {
        let tree = &self.checker.module().ast;
        let span = tree.exprs.span(target);
        if !matches!(tree.exprs[target], ast::Expr::Name) {
            self.checker
                .not_supported(span, "assignments to targets other than names");
            return None;
        }
        let name = tree.file.slice(span);
        match self.resolve(span)? {
            Symbol::Local(Local::Variable(variable)) => Some(variable),
            Symbol::Local(Local::Constant(constant)) => {
                let declared = self.body.constants.span(constant);
                self.checker.report(
                    Diagnostic::new(
                        diagnostics::CONSTANT_REASSIGNMENT,
                        format!("cannot assign to constant `{name}`"),
                    )
                    .label(&tree.file, span, "")
                    .label(&tree.file, declared, "declared as a constant here"),
                );
                None
            }
            Symbol::Constant(_) => {
                let message = format!("cannot assign to constant `{name}`");
                self.checker
                    .error(diagnostics::CONSTANT_REASSIGNMENT, message, span);
                None
            }
            _ => {
                let message = format!("cannot assign to `{name}`, which is not a variable");
                self.checker
                    .error(diagnostics::INVALID_ASSIGNMENT, message, span);
                None
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use model::DefId;
    use model::ast;
    use model::thir::Body;
    use source::SourceFile;

    use crate::testing::Reported;
    use crate::testing::reported;
    use crate::testing::with_checker;

    /// The THIR of every function body of `text`, as text, and the
    /// diagnostics.
    fn bodies(text: &str) -> (Vec<String>, Vec<Reported>) {
        let ((bodies, declared), types, reported) = with_checker(text, |checker, main| {
            let tree = &checker.modules[main.index()].ast;
            let declarations: Vec<_> = tree.lists.items(tree.top_level).collect();
            let declared = declarations.len();
            let mut bodies = Vec::new();
            for (index, declaration) in declarations.into_iter().enumerate() {
                let ast::Decl::Function(function) = tree.decls[declaration] else {
                    continue;
                };
                let definition = DefId {
                    module: main,
                    index: index as u32,
                };
                let mut body = Body::default();
                let checked = checker.check_body(definition, function.name.span, &mut body, false);
                if checked && !body.has_errors {
                    bodies.push(body);
                }
            }
            (bodies, declared)
        });
        let file = SourceFile::new("main.peep", text);
        // A called function's name, by its place among the declarations.
        let names: Vec<String> = (0..declared).map(|index| format!("f{index}")).collect();
        let name = |function: DefId| names[function.index as usize].as_str();
        let shown = bodies
            .iter()
            .map(|body| body.text(&types, &file, name))
            .collect();
        (shown, reported)
    }

    #[test]
    fn statements_and_operators() {
        let (bodies, reported) = bodies(
            "fn f(a: i32, mut b: i64) -> i64 {
                let c = a + 1;
                b = b + c;
                const K: i64 = 2 * 3;
                if a < 0 { return K; } else if !(a == 1) { b = -b; } else { let c = true; }
                let d: u8;
                return b << 2u8;
            }",
        );
        assert!(reported.is_empty(), "{reported:?}");
        assert_eq!(
            bodies,
            ["    let c.2 = (a.0 Add 1:i32)
    b.1 = (b.1 Add convert(c.2):i64)
    if (a.0 LessThan 0:i32) {
        return 6:i64
    } else if (LogicalNot (a.0 EqualTo 1:i32)) {
        b.1 = (Negate b.1)
    } else {
        let c.3 = true
    }
    let d.4
    return (b.1 ShiftLeft 2:u8)
"]
        );
    }

    /// A range's literal start takes the end's type; its variables are
    /// declared in the body. Its step is the ends' type made unsigned, 1
    /// when not written.
    #[test]
    fn loops() {
        let (bodies, reported) = bodies(
            "fn f(n: i64) {
                for { break; }
                for n > 0 { continue; }
                for i in 0..n { let j = i; }
                for 0..3 { }
                for k in 1..=n { }
                for i, m in n..0:2 { }
            }",
        );
        assert!(reported.is_empty(), "{reported:?}");
        assert_eq!(
            bodies,
            ["    for {
        break
    }
    for (n.0 GreaterThan 0:i64) {
        continue
    }
    for i.1 in 0:i64..n.0 by 1:u64 {
        let j.2 = i.1
    }
    for 0:i32..3:i32 by 1:u32 {
    }
    for k.3 in 1:i64..=n.0 by 1:u64 {
    }
    for i.4, m.5 in n.0..0:i64 by 2:u64 {
    }
"]
        );
    }

    #[test]
    fn names_shadow_until_their_block_ends() {
        let (bodies, reported) = bodies(
            "fn f(x: i32) {
                { let x = x + 1; let y = x; }
                { let x = true; let z = x; }
                let w = x;
                { const x = 2u8; let v = x; }
            }",
        );
        assert!(reported.is_empty(), "{reported:?}");
        assert_eq!(
            bodies,
            ["    {
        let x.1 = (x.0 Add 1:i32)
        let y.2 = x.1
    }
    {
        let x.3 = true
        let z.4 = x.3
    }
    let w.5 = x.0
    {
        let v.6 = 2:u8
    }
"]
        );
    }

    /// Types and values share names, and a value can't take a type's name,
    /// declared or built-in: the name stays the type's.
    #[test]
    fn types_and_values_share_names() {
        let (_, found) = bodies(
            "type Meters = i64;
            fn f() {
                let Meters = 1;
                let far: Meters = 2;
                let i32 = 3;
                let near: i32 = 4;
                let n = i32;
            }",
        );
        let taken = "is a type; a value can't take its name";
        let expected = reported(&[
            ("T0003", &format!("`Meters` {taken}"), "Meters"),
            ("T0003", &format!("`i32` {taken}"), "i32"),
            ("T0004", "`i32` is a type, not a value", "i32"),
        ]);
        assert_eq!(found, expected);
    }

    /// A block declares each name once; the parameters are in the body's
    /// outermost block. The first declaration keeps the name.
    #[test]
    fn a_block_declares_a_name_once() {
        let (shown, found) = bodies(
            "fn f(a: i32, a: i64) {
                let b = 1;
                let b = true;
                const C = 1;
                let C = 2;
                let a = 3;
                let c: i32 = b;
            }",
        );
        let again = "is already declared in this block";
        let expected = reported(&[
            ("T0003", &format!("`a` {again}"), "a"),
            ("T0003", &format!("`b` {again}"), "b"),
            ("T0003", &format!("`C` {again}"), "C"),
            ("T0003", &format!("`a` {again}"), "a"),
        ]);
        assert_eq!(found, expected);
        assert!(shown.is_empty(), "{shown:?}");
    }

    #[test]
    fn constants_fold() {
        let (bodies, reported) = bodies(
            "fn g() {
                let small: i8 = -128;
                let wrapped = 127i8 + 1;
                const N: u8 = 200;
                let wide: i16 = N;
                let precise: f32 = 0.1 + 0.2;
                let whole: f64 = 1;
                let big = 1u256 << 100u8;
                let no = !(1 < 2);
                let parenthesized = (2 + 3) * -(4);
            }",
        );
        assert!(reported.is_empty(), "{reported:?}");
        // Folded in `f32`: in `f64` it would not be an `f32` and would show
        // as 0.30000000000000004.
        let precise = 0.1f32 + 0.2f32;
        assert_eq!(
            bodies,
            [format!(
                "    let small.0 = -128:i8
    let wrapped.1 = -128:i8
    let wide.2 = 200:i16
    let precise.3 = {precise}:f32
    let whole.4 = 1:f64
    let big.5 = {}:u256
    let no.6 = false
    let parenthesized.7 = -20:i32
",
                1u128 << 100
            )]
        );
    }

    #[test]
    fn errors_are_located() {
        let (_, found) = bodies(
            "fn f(p: i32) -> i32 {
                let a: u8 = 300;
                let b: u8 = -1;
                let c: i8 = -(128);
                let d = 1i32 + 2i64;
                let e = true + false;
                let g = 1 / 0;
                let h = 1u8 << 8;
                let i = 1 << 1.5;
                let u = p / 0;
                let v = p << 40u8;
                let j = missing;
                let k = utils;
                let l = helper;
                let m = 1;
                m = 3;
                let w = m(1);
                let y = utils::Nope;
                let z = nothing(missing);
                const C = 1; C = 2;
                const D: i32 = p;
                let n;
                if 1 {}
                let s = \"a\";
                break;
                let q = 5f16;
                let r: f64 = 0xff;
                let t = -true;
                { let m = true; }
                alloc(1);
                let .{ first, second as renamed } = 1;
                let after = first + renamed;
                return;
            }
            fn helper() { return 1; }
            fn calls_back(callback: fn(i32)) { callback(1); }",
        );
        let expected = reported(&[
            ("L0003", "literal `300` does not fit `u8`", "300"),
            ("L0003", "literal `-1` does not fit `u8`", "-1"),
            ("L0003", "literal `128` does not fit `i8`", "128"),
            (
                "T0001",
                "mismatched types: expected `i32`, found `i64`",
                "2i64",
            ),
            (
                "T0004",
                "operator `+` cannot be applied to `bool`",
                "true + false",
            ),
            ("T0004", "division by zero", "0"),
            ("T0004", "a shift count must be from 0 to 7", "8"),
            (
                "T0004",
                "a shift count must be an integer, found `f64`",
                "1.5",
            ),
            ("T0004", "division by zero", "0"),
            ("T0004", "a shift count must be from 0 to 31", "40u8"),
            ("T0002", "unknown name `missing`", "missing"),
            ("T0004", "`utils` is a module, not a value", "utils"),
            (
                "T0043",
                "functions as values are not supported yet",
                "helper",
            ),
            ("T0007", "cannot assign to immutable `m`", "m"),
            ("T0005", "`m` is not a function", "m"),
            ("T0002", "unknown name `Nope` in module `utils`", "Nope"),
            ("T0002", "unknown name `nothing`", "nothing"),
            ("T0002", "unknown name `missing`", "missing"),
            ("T0018", "cannot assign to constant `C`", "C"),
            (
                "T0004",
                "the value of a constant must be known at compile time",
                "p",
            ),
            ("P0007", "`n` needs a type or a value", "n"),
            (
                "T0001",
                "mismatched types: expected `bool`, found `i32`",
                "1",
            ),
            ("T0043", "string literals are not supported yet", "\"a\""),
            ("P0004", "`break` outside a loop", "break;"),
            ("S0007", "unsupported numeric literal suffix `f16`", "5f16"),
            (
                "T0001",
                "mismatched types: expected `f64`, found `i32`",
                "0xff",
            ),
            ("T0004", "operator `-` cannot be applied to `bool`", "-true"),
            ("T0043", "built-in functions are not supported yet", "alloc"),
            (
                "T0043",
                "destructuring `let`s are not supported yet",
                "let .{ first, second as renamed } = 1;",
            ),
            ("T0016", "`return` needs a value of type `i32`", "return;"),
            (
                "T0016",
                "this function has no result, so `return` takes no value",
                "1",
            ),
            ("T0043", "function types are not supported yet", "fn(i32)"),
        ]);
        assert_eq!(found, expected);
    }

    /// The diagnostics of the body of `text`'s first function, as each
    /// message with its first note.
    fn notes(text: &str) -> Vec<(String, Option<String>)> {
        let (notes, _, _) = with_checker(text, |checker, main| {
            let tree = &checker.modules[main.index()].ast;
            let declaration = tree.lists.items(tree.top_level).next().unwrap();
            let ast::Decl::Function(function) = tree.decls[declaration] else {
                unreachable!("the first declaration is a function")
            };
            let definition = DefId {
                module: main,
                index: 0,
            };
            checker.check_body(definition, function.name.span, &mut Body::default(), false);
            let note = |diagnostic: &diagnostics::Diagnostic| {
                let first = diagnostic.notes.first();
                (
                    diagnostic.message.clone(),
                    first.map(|note| note.text.clone()),
                )
            };
            checker.module_checks[main.index()]
                .diagnostics
                .iter()
                .map(note)
                .collect()
        });
        notes
    }

    #[test]
    fn a_left_literal_gets_help_if_it_can_have_the_type() {
        let mismatch = |found: &str| format!("mismatched types: expected `i32`, found `{found}`");
        let help = |literal: &str| {
            Some(format!(
                "write `{literal}` to give the left literal that type"
            ))
        };
        // Without a literal to fix, the right operand is converted with `as`.
        let cast = |ty: &str| {
            Some(format!(
                "add `as {ty}` to convert, which can change the value"
            ))
        };
        assert_eq!(
            notes(
                "fn f(x: i64, y: f64) { let a = 1 + x; let b = x + 1; let c = 1 + y; let d = 0xff + y; }"
            ),
            [
                (mismatch("i64"), help("1i64")),
                (mismatch("f64"), help("1f64")),
                (mismatch("f64"), cast("i32")),
            ]
        );
        assert_eq!(
            notes("fn f(x: i64) { let a = 1 + true; let b = 1.5 + x; }"),
            [
                (mismatch("bool"), cast("i32")),
                (
                    "mismatched types: expected `f64`, found `i64`".to_string(),
                    cast("f64")
                ),
            ]
        );
    }

    /// A name of another module is guessed only among those it exports.
    #[test]
    fn unknown_members_get_exported_suggestions() {
        assert_eq!(
            notes("fn f() { utils::Shwn(); utils::hiddn(); }"),
            [
                (
                    "unknown name `Shwn` in module `utils`".to_string(),
                    Some("did you mean `Shown`?".to_string())
                ),
                ("unknown name `hiddn` in module `utils`".to_string(), None),
            ]
        );
    }

    #[test]
    fn unknown_names_get_suggestions() {
        assert_eq!(
            notes("fn f(count: i32) { let total = cuont; }"),
            [(
                "unknown name `cuont`".to_string(),
                Some("did you mean `count`?".to_string())
            )]
        );
    }
}
