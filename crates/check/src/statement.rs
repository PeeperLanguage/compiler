//! Checking a function body into THIR: its statements, and the names its
//! blocks declare.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::ast;
use model::ast::Ident;
use model::constant::Constant;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Block;
use model::thir::Body;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::Name;
use model::thir::Stmt;
use model::thir::Variable;
use source::Span;

use crate::ModuleChecker;
use crate::scope::report_redeclared;
use model::Local;
use model::Symbol;

/// Building the THIR of one piece of code: a function's body, or a
/// constant's value. It checks the code as it builds it.
pub(crate) struct ThirBuilder<'a, 'p> {
    /// The module's checker, borrowed for the build: its module, queries and
    /// diagnostics.
    pub(crate) checker: ModuleChecker<'a, 'p>,
    pub(crate) body: &'a mut Body,
    /// The names written in the body, in the order checked, for a caller that
    /// wants them (`ModuleChecker::check_body`); `None` records nothing.
    recorded: Option<&'a mut Vec<Name>>,
    /// The names declared in the enclosing blocks so far, innermost last. Few
    /// names are in scope at once, so a search from the end beats hashing.
    pub(crate) names: Vec<(&'p str, Local)>,
    /// Where in `names` the current block's names start.
    block_start: usize,
    /// The current block, for the variables it declares.
    block: Id<Block>,
    result: TypeId, // the function's result type
}

impl ModuleChecker<'_, '_> {
    /// Puts in `body` the THIR of `function`'s body, given its type `ty`
    /// (`function`), with `has_errors` set when checking it reported an
    /// error; what `body` held is dropped, its memory kept. False, with
    /// `body` untouched, for a function without a body or whose signature had
    /// an error. The names written in it go to `names` when given, as for an
    /// editor.
    pub fn check_body(
        &mut self,
        function: &ast::Function,
        ty: TypeId,
        body: &mut Body,
        names: Option<&mut Vec<Name>>,
    ) -> bool {
        let Some(block) = function.body else {
            return false;
        };
        let reported = self.diagnostics.len();
        let Type::Function { parameters, result } = &self.queries.types[ty] else {
            return false; // the signature's error was reported
        };
        let (parameters, result) = (parameters.clone(), *result);
        let tree = &self.module().ast;
        body.clear();
        let mut builder = ThirBuilder::new(self, body, result, names);
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
                },
                name.span,
            );
            builder.declare(name, Local::Variable(variable));
        }
        let statements = builder.check_statements(tree.blocks[block].statements);
        builder.body.blocks[outermost].statements = statements;
        // A part of the signature that failed was reported with the signature.
        body.has_errors = parameters.contains(&TypeId::INVALID)
            || result == TypeId::INVALID
            || self.diagnostics[reported..]
                .iter()
                .any(Diagnostic::is_error);
        true
    }

    /// The type and value of the module's constant `declaration`; `None` when
    /// it had an error. Its value is checked as a body's `const` is, with no
    /// variables in scope.
    pub fn check_constant(&mut self, declaration: Id<ast::Decl>) -> Option<Constant> {
        let mut body = Body::default();
        ThirBuilder::new(self, &mut body, TypeId::VOID, None).check_const_statement(declaration)
    }
}

impl<'a, 'p> ThirBuilder<'a, 'p> {
    /// Building code into `body` whose `return` gives `result`, recording
    /// its names into `recorded` when given.
    fn new(
        checker: &'a mut ModuleChecker<'_, 'p>,
        body: &'a mut Body,
        result: TypeId,
        recorded: Option<&'a mut Vec<Name>>,
    ) -> ThirBuilder<'a, 'p> {
        ThirBuilder {
            checker: ModuleChecker {
                current_module: checker.current_module,
                queries: checker.queries,
                diagnostics: checker.diagnostics,
            },
            body,
            recorded,
            names: Vec::new(),
            block_start: 0,
            block: Id::from_index(0), // the function's, added first (`ModuleChecker::check_body`)
            result,
        }
    }

    /// The THIR of `statements`, in the current block.
    fn check_statements(&mut self, statements: List<ast::Stmt>) -> List<Stmt> {
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(statements.len());
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
        let checked = Block {
            parent: Some(self.block),
            statements: List::EMPTY,
        };
        let checked = self
            .body
            .add(checked, self.checker.module().ast.blocks.span(block));
        let names_before = self.names.len();
        let enclosing_start = std::mem::replace(&mut self.block_start, names_before);
        let enclosing = std::mem::replace(&mut self.block, checked);
        let statements = self.check_statements(self.checker.module().ast.blocks[block].statements);
        self.names.truncate(names_before);
        self.block_start = enclosing_start;
        self.block = enclosing;
        self.body.blocks[checked].statements = statements;
        checked
    }

    /// Declares `name` in the current block. It may
    /// shadow a name of an outer block until the block ends. A name the block
    /// already declares is reported and keeps its first meaning; the
    /// parameters are in the body's outermost block, as in Go.
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
            self.checker.diagnostics.push(diagnostic);
            return;
        }
        self.record(name.span, Symbol::Local(local));
        self.names.push((text, local));
    }

    /// Records that the name written at `span` stands for `symbol`, when the
    /// caller asked for names (`ModuleChecker::check_body`).
    pub(crate) fn record(&mut self, span: Span, symbol: Symbol) {
        if let Some(names) = &mut self.recorded {
            names.push(Name { span, symbol });
        }
    }

    /// What the name written at `span` stands for: a name of the current
    /// block or an outer one, else of the module or the prelude, recorded
    /// (`record`). `None` after reporting that nothing declares it.
    pub(crate) fn resolve(&mut self, span: Span) -> Option<Symbol> {
        let name = self.checker.module().ast.file.slice(span);
        let found = self
            .names
            .iter()
            .rev()
            .find(|&&(declared, _)| declared == name);
        let symbol = match found {
            Some(&(_, local)) => Symbol::Local(local),
            None => match self.checker.global(name) {
                Some(symbol) => symbol,
                None => {
                    self.report_undeclared(name, span);
                    return None;
                }
            },
        };
        self.record(span, symbol);
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
            ast::Stmt::Expression(value) => Stmt::Expression(self.add_expr(value)),
            ast::Stmt::Assign { target, value } => self.assign(target, value)?,
            ast::Stmt::If {
                condition,
                then,
                otherwise,
            } => {
                let condition = self.add_expr_as(condition, TypeId::BOOL);
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
                        };
                        let variable = self.body.add(unknown, name.span);
                        self.declare(name, Local::Variable(variable));
                    }
                }
                return None;
            }
            ast::Stmt::Loop { .. } | ast::Stmt::ForIn { .. } => {
                self.checker.not_supported(span, "loops");
                return None;
            }
            ast::Stmt::Break | ast::Stmt::Continue => {
                self.checker.not_supported(span, "`break` and `continue`");
                return None;
            }
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
        let declared = ty.map(|ty| self.checker.resolve_type(ty));
        let initialized = value.is_some();
        let (ty, value) = match (declared, value) {
            (Some(ty), value) => (ty, value.map(|value| self.add_expr_as(value, ty))),
            (None, Some(value)) => {
                let value = self.add_expr(value);
                let ty = self.body.exprs[value].ty;
                if ty != TypeId::VOID {
                    (ty, Some(value))
                } else {
                    let span = self.body.exprs.span(value);
                    let message = "this expression has no value to bind";
                    self.checker
                        .error(diagnostics::INVALID_OPERATION, message, span);
                    (TypeId::INVALID, None)
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
            self.checker.diagnostics.push(
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

    /// `const name: ty = value;`: a name for a value known at compile time.
    /// Without a type, the constant takes its value's type. Gives its type and
    /// value, or `None` after an error.
    fn check_const_statement(&mut self, declaration: Id<ast::Decl>) -> Option<Constant> {
        let ast::Decl::Const {
            name, ty, value, ..
        } = self.checker.module().ast.decls[declaration]
        else {
            unreachable!("`{declaration:?}` is a const declaration")
        };
        let declared = ty.map(|ty| self.checker.resolve_type(ty));
        let Some(value) = value else {
            let text = name.text(&self.checker.module().ast.file);
            let message = format!("constant `{text}` needs a value");
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, name.span);
            return None;
        };
        let span = self.checker.module().ast.exprs.span(value);
        let checked = match declared {
            Some(ty) => self.check_expr_as(value, ty),
            None => self.check_expr(value, None),
        };
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
                self.add_expr(value); // for its own errors
                None
            }
            Some(value) => Some(self.add_expr_as(value, self.result)),
            None => {
                if self.result != TypeId::VOID && self.result != TypeId::INVALID {
                    let result = self.checker.queries.type_text(self.result);
                    let message = format!("`return` needs a value of type `{result}`");
                    self.checker
                        .error(diagnostics::INVALID_RETURN, message, span);
                }
                None
            }
        }
    }

    /// `target = value;`, where the target is a mutable variable.
    fn assign(&mut self, target: Id<ast::Expr>, value: Id<ast::Expr>) -> Option<Stmt> {
        let Some(variable) = self.assigned_variable(target) else {
            self.add_expr(value); // for its own errors
            return None;
        };
        let value = self.add_expr_as(value, self.body.variables[variable].ty);
        Some(Stmt::Assign { variable, value })
    }

    /// The variable that `target` names, reporting a target that cannot be
    /// assigned. An immutable variable is reported but still given, so the
    /// value is checked against its type. An immutable variable declared
    /// without a value may be assigned; the path checks on MIR report one that may be
    /// assigned twice.
    fn assigned_variable(&mut self, target: Id<ast::Expr>) -> Option<Id<Variable>> {
        let tree = &self.checker.module().ast;
        let span = tree.exprs.span(target);
        if !matches!(tree.exprs[target], ast::Expr::Name) {
            self.checker
                .not_supported(span, "assignments to targets other than names");
            return None;
        }
        let name = tree.file.slice(span);
        let variable = match self.resolve(span)? {
            Symbol::Local(Local::Variable(variable)) => variable,
            Symbol::Local(Local::Constant(constant)) => {
                let declared = self.body.constants.span(constant);
                self.checker.diagnostics.push(
                    Diagnostic::new(
                        diagnostics::CONSTANT_REASSIGNMENT,
                        format!("cannot assign to constant `{name}`"),
                    )
                    .label(&tree.file, span, "")
                    .label(&tree.file, declared, "declared as a constant here"),
                );
                return None;
            }
            Symbol::Constant(_) => {
                let message = format!("cannot assign to constant `{name}`");
                self.checker
                    .error(diagnostics::CONSTANT_REASSIGNMENT, message, span);
                return None;
            }
            _ => {
                let message = format!("cannot assign to `{name}`, which is not a variable");
                self.checker
                    .error(diagnostics::INVALID_ASSIGNMENT, message, span);
                return None;
            }
        };
        let Variable {
            mutable,
            initialized,
            ..
        } = self.body.variables[variable];
        if !mutable && initialized {
            self.checker.diagnostics.push(
                Diagnostic::new(
                    diagnostics::INVALID_ASSIGNMENT,
                    format!("cannot assign to immutable `{name}`"),
                )
                .label(&tree.file, span, "")
                .label(
                    &tree.file,
                    self.body.variables.span(variable),
                    "declared without `mut` here",
                ),
            );
        }
        Some(variable)
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
        let (bodies, types, reported) = with_checker(text, |checker| {
            let tree = &checker.module().ast;
            let declarations: Vec<_> = tree.lists.items(tree.top_level).collect();
            let mut bodies = Vec::new();
            for declaration in declarations {
                let ast::Decl::Function(function) = checker.module().ast.decls[declaration] else {
                    continue;
                };
                let signature = checker.check_signature(declaration);
                let mut body = Body::default();
                if checker.check_body(&function, signature, &mut body, None) && !body.has_errors {
                    bodies.push(body);
                }
            }
            bodies
        });
        let file = SourceFile::new("main.peep", text);
        let name = |function: DefId| format!("f{}", function.index);
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
    b.1 = (b.1 Add widen(c.2):i64)
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
        let precise = f64::from(0.1f32 + 0.2f32);
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
                for {}
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
            ("T0043", "loops are not supported yet", "for {}"),
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
        let (notes, _, _) = with_checker(text, |checker| {
            let tree = &checker.module().ast;
            let declaration = tree.lists.items(tree.top_level).next().unwrap();
            let ast::Decl::Function(function) = tree.decls[declaration] else {
                unreachable!("the first declaration is a function")
            };
            let signature = checker.check_signature(declaration);
            checker.check_body(&function, signature, &mut Body::default(), None);
            let note = |diagnostic: &diagnostics::Diagnostic| {
                let first = diagnostic.notes.first();
                (
                    diagnostic.message.clone(),
                    first.map(|note| note.text.clone()),
                )
            };
            checker.diagnostics.iter().map(note).collect()
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
        assert_eq!(
            notes(
                "fn f(x: i64, y: f64) { let a = 1 + x; let b = x + 1; let c = 1 + y; let d = 0xff + y; }"
            ),
            [
                (mismatch("i64"), help("1i64")),
                (mismatch("f64"), help("1f64")),
                (mismatch("f64"), None),
            ]
        );
        assert_eq!(
            notes("fn f(x: i64) { let a = 1 + true; let b = 1.5 + x; }"),
            [
                (mismatch("bool"), None),
                (
                    "mismatched types: expected `f64`, found `i64`".to_string(),
                    None
                ),
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
