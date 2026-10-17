//! Checking a function body into THIR: its statements, and the names its
//! blocks declare.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use model::syntax;
use model::thir::Block;
use model::thir::Body;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::Local;
use model::thir::Stmt;
use model::types::Type;
use model::types::TypeId;
use model::value::Constant;
use source::Span;

use crate::Checker;
use crate::Queries;
use crate::scope::Binding;

/// What a name declared in a body stands for.
#[derive(Clone)]
pub(crate) enum Named {
    Local(Id<Local>),
    /// a `const`, by its type and value; `None` when its value had an error,
    /// so uses of it report nothing more
    Constant(Option<Constant>),
}

/// Checking one function body.
pub(crate) struct BodyChecker<'c, 'a, Q: Queries> {
    pub(crate) checker: &'c mut Checker<'a, Q>,
    pub(crate) tree: &'a syntax::Tree,
    pub(crate) body: Body,
    /// The names declared in the enclosing blocks so far, innermost last, with
    /// where each is declared. Few names are in scope at once, so a search
    /// from the end beats hashing.
    pub(crate) names: Vec<(&'a str, Span, Named)>,
    /// Where in `names` the current block's names start.
    block_start: usize,
    result: TypeId, // the function's result type
}

impl<'a, Q: Queries> Checker<'a, Q> {
    /// The THIR of `function`'s body, given its signature `ty` (`signature`).
    /// `None` for a function without a body, or whose signature or body had an
    /// error: what later phases would find in it would only follow from that.
    pub fn body(&mut self, function: &syntax::Function, ty: TypeId) -> Option<Body> {
        let block = function.body?;
        let reported = self.diagnostics.len();
        let Type::Function { parameters, result } = &self.queries.types()[ty] else {
            return None; // the signature's error was reported
        };
        let (parameters, result) = (parameters.clone(), *result);
        let tree = &self.module.syntax;
        let mut checker = BodyChecker::new(self, result);
        for (parameter, &ty) in tree.lists.items(function.parameters).zip(&parameters) {
            let parameter = tree.params[parameter];
            let name = parameter.name.unwrap_or_default();
            let local = checker.body.add(
                Local {
                    ty,
                    mutable: parameter.mutable,
                    declared_without_value: false,
                },
                name,
            );
            checker.declare(name, Named::Local(local));
        }
        checker.body.statements = checker.statements(tree.blocks[block].statements);
        let body = checker.body;
        let failed = self.diagnostics[reported..]
            .iter()
            .any(Diagnostic::is_error);
        (!failed).then_some(body)
    }

    /// The type and value of the module's constant `declaration`; `None` when
    /// it had an error. Its value is checked as a body's `const` is, with no
    /// locals in scope.
    pub fn constant(&mut self, declaration: Id<syntax::Decl>) -> Option<Constant> {
        BodyChecker::new(self, TypeId::VOID).constant_value(declaration)
    }
}

impl<'c, 'a, Q: Queries> BodyChecker<'c, 'a, Q> {
    /// Checking a body of a function whose result type is `result`.
    fn new(checker: &'c mut Checker<'a, Q>, result: TypeId) -> BodyChecker<'c, 'a, Q> {
        let tree = &checker.module.syntax;
        BodyChecker {
            checker,
            tree,
            body: Body::default(),
            names: Vec::new(),
            block_start: 0,
            result,
        }
    }

    /// The THIR of `statements`, in the current block.
    fn statements(&mut self, statements: List<syntax::Stmt>) -> List<Stmt> {
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(statements.len());
        for statement in self.tree.lists.items(statements) {
            if let Some(statement) = self.statement(statement) {
                checked.push(statement.into());
            }
        }
        self.body.lists.add(&checked)
    }

    /// The THIR of `block`; the names it declares end with it.
    fn block(&mut self, block: Id<syntax::Block>) -> Id<Block> {
        let names_before = self.names.len();
        let enclosing_start = std::mem::replace(&mut self.block_start, names_before);
        let statements = self.statements(self.tree.blocks[block].statements);
        self.names.truncate(names_before);
        self.block_start = enclosing_start;
        self.body
            .add(Block { statements }, self.tree.blocks.span(block))
    }

    /// Declares the name written at `span` in the current block. It may
    /// shadow a name of an outer block until the block ends. A name the block
    /// already declares is reported and keeps its first meaning; the
    /// parameters are in the body's outermost block, as in Go.
    fn declare(&mut self, span: Span, named: Named) {
        let name = self.tree.file.slice(span);
        if name.is_empty() || name == "_" {
            return; // missing after a syntax error, or not a name
        }
        let block = &self.names[self.block_start..];
        if let Some(&(_, first, _)) = block.iter().find(|(declared, ..)| *declared == name) {
            let file = &self.tree.file;
            self.checker.diagnostics.push(
                Diagnostic::new(
                    diagnostics::ALREADY_DECLARED,
                    format!("`{name}` is already declared in this block"),
                )
                .label(file, span, "declared again here")
                .label(file, first, "first declared here"),
            );
            return;
        }
        self.names.push((name, span, named));
    }

    /// What `name` stands for in the current block, with where it is declared.
    pub(crate) fn lookup(&self, name: &str) -> Option<(Span, &Named)> {
        let found = self
            .names
            .iter()
            .rev()
            .find(|&&(declared, ..)| declared == name);
        found.map(|(_, span, named)| (*span, named))
    }

    /// The THIR of `statement`; `None` for a `const`, which only declares a
    /// name, and for a statement with an error.
    fn statement(&mut self, statement: Id<syntax::Stmt>) -> Option<Id<Stmt>> {
        let tree = self.tree;
        let span = tree.stmts.span(statement);
        let checked = match tree.stmts[statement] {
            syntax::Stmt::Block(block) => Stmt::Block(self.block(block)),
            syntax::Stmt::Let {
                mutable,
                name,
                ty,
                value,
            } => self.let_statement(mutable, name, ty, value),
            syntax::Stmt::Const(declaration) => {
                let constant = self.constant_value(declaration);
                let syntax::Decl::Const { name, .. } = tree.decls[declaration] else {
                    unreachable!("a const statement holds a const declaration")
                };
                self.declare(name, Named::Constant(constant));
                return None;
            }
            syntax::Stmt::Return(value) => Stmt::Return(self.return_value(span, value)),
            syntax::Stmt::Expression(value) => Stmt::Expression(self.value(value, None)),
            syntax::Stmt::Assign { target, value } => self.assign(target, value)?,
            syntax::Stmt::If {
                condition,
                then,
                otherwise,
            } => {
                let condition = self.expect(condition, TypeId::BOOL);
                let then = self.block(then?); // missing after a syntax error
                let otherwise = otherwise.and_then(|otherwise| self.statement(otherwise));
                Stmt::If {
                    condition,
                    then,
                    otherwise,
                }
            }
            syntax::Stmt::Destructure { pattern, .. } => {
                self.checker.not_supported(span, "destructuring `let`s");
                // Its names are still declared, so their uses report nothing
                // more.
                let syntax::Pattern::Struct(fields) = tree.patterns[pattern] else {
                    return None; // the parser reported it
                };
                for field in tree.lists.items(fields) {
                    if let syntax::Pattern::Field { field, binding } = tree.patterns[field] {
                        let name = binding.unwrap_or(field);
                        let unknown = Local {
                            ty: TypeId::INVALID,
                            mutable: true,
                            declared_without_value: false,
                        };
                        let local = self.body.add(unknown, name);
                        self.declare(name, Named::Local(local));
                    }
                }
                return None;
            }
            syntax::Stmt::Loop { .. } | syntax::Stmt::ForIn { .. } => {
                self.checker.not_supported(span, "loops");
                return None;
            }
            syntax::Stmt::Break | syntax::Stmt::Continue => {
                self.checker.not_supported(span, "`break` and `continue`");
                return None;
            }
            syntax::Stmt::Match { .. } => {
                self.checker.not_supported(span, "`match` statements");
                return None;
            }
        };
        Some(self.body.add(checked, span))
    }

    /// `let mut name: ty = value;`. The local is declared after its value is
    /// checked, so the value cannot use it.
    fn let_statement(
        &mut self,
        mutable: bool,
        name: Span,
        ty: Option<Id<syntax::Type>>,
        value: Option<Id<syntax::Expr>>,
    ) -> Stmt {
        let declared = ty.map(|ty| self.checker.resolve_type(ty));
        let declared_without_value = value.is_none();
        let (ty, value) = match (declared, value) {
            (Some(ty), value) => (ty, value.map(|value| self.expect(value, ty))),
            (None, Some(value)) => {
                let value = self.value(value, None);
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
                let text = self.tree.file.slice(name);
                let message = format!("`{text}` needs a type or a value");
                self.checker.error(diagnostics::MISSING_TYPE, message, name);
                (TypeId::INVALID, None)
            }
        };
        let local = Local {
            ty,
            mutable,
            declared_without_value,
        };
        let text = self.tree.file.slice(name);
        let shadowed = self
            .names
            .iter()
            .rposition(|&(declared, ..)| declared == text);
        if let Some(position) = shadowed
            && position < self.block_start
            && let Named::Local(outer) = self.names[position].2
            && self.body.locals[outer].mutable
        {
            // Likely meant as `x = ...`: the outer local stays unchanged.
            let file = &self.tree.file;
            self.checker.diagnostics.push(
                Diagnostic::new(
                    diagnostics::HIDDEN_MUTABLE,
                    format!("this `let` hides the outer `mut {text}`"),
                )
                .label(
                    file,
                    name,
                    format!("a new `{text}`, only inside this block"),
                )
                .label(
                    file,
                    self.body.locals.span(outer),
                    format!("the outer `{text}`, declared `mut` here"),
                )
                .help(
                    format!("to change the outer `{text}`, write `{text} = ...` without `let`"),
                    Vec::new(),
                ),
            );
        }
        let local = self.body.add(local, name);
        self.declare(name, Named::Local(local));
        Stmt::Let { local, value }
    }

    /// `const name: ty = value;`: a name for a value known at compile time.
    /// Without a type, the constant takes its value's type. Gives its type and
    /// value, or `None` after an error.
    fn constant_value(&mut self, declaration: Id<syntax::Decl>) -> Option<Constant> {
        let syntax::Decl::Const {
            name, ty, value, ..
        } = self.tree.decls[declaration]
        else {
            unreachable!("`{declaration:?}` is a const declaration")
        };
        let declared = ty.map(|ty| self.checker.resolve_type(ty));
        let Some(value) = value else {
            let text = self.tree.file.slice(name);
            let message = format!("constant `{text}` needs a value");
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, name);
            return None;
        };
        let span = self.tree.exprs.span(value);
        let checked = match declared {
            Some(ty) => self.coerced(value, ty),
            None => self.expr(value, None),
        };
        match checked.kind {
            ExprKind::Constant(value) => Some((checked.ty, value)),
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
    fn return_value(&mut self, span: Span, value: Option<Id<syntax::Expr>>) -> Option<Id<Expr>> {
        match value {
            Some(value) if self.result == TypeId::VOID => {
                let message = "this function has no result, so `return` takes no value";
                self.checker.error(
                    diagnostics::INVALID_RETURN,
                    message,
                    self.tree.exprs.span(value),
                );
                self.value(value, None); // for its own errors
                None
            }
            Some(value) => Some(self.expect(value, self.result)),
            None => {
                if self.result != TypeId::VOID && self.result != TypeId::INVALID {
                    let result = self.checker.queries.types().text(self.result);
                    let message = format!("`return` needs a value of type `{result}`");
                    self.checker
                        .error(diagnostics::INVALID_RETURN, message, span);
                }
                None
            }
        }
    }

    /// `target = value;`, where the target is a mutable local.
    fn assign(&mut self, target: Id<syntax::Expr>, value: Id<syntax::Expr>) -> Option<Stmt> {
        let Some(local) = self.assigned_local(target) else {
            self.value(value, None); // for its own errors
            return None;
        };
        let value = self.expect(value, self.body.locals[local].ty);
        Some(Stmt::Assign { local, value })
    }

    /// The local that `target` names, reporting a target that cannot be
    /// assigned. An immutable local is reported but still given, so the value
    /// is checked against its type. An immutable local declared without a
    /// value may be assigned; the path checks on MIR report one that may be
    /// assigned twice.
    fn assigned_local(&mut self, target: Id<syntax::Expr>) -> Option<Id<Local>> {
        let tree = self.tree;
        let span = tree.exprs.span(target);
        if !matches!(tree.exprs[target], syntax::Expr::Name) {
            self.checker
                .not_supported(span, "assignments to targets other than names");
            return None;
        }
        let name = tree.file.slice(span);
        let (declared, local) = match self.lookup(name) {
            Some((declared, &Named::Local(local))) => (declared, local),
            Some((declared, Named::Constant(_))) => {
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
            None => {
                let (code, message) = match self.checker.global(name) {
                    Some(Binding::Constant(_)) => (
                        diagnostics::CONSTANT_REASSIGNMENT,
                        format!("cannot assign to constant `{name}`"),
                    ),
                    Some(_) => (
                        diagnostics::INVALID_ASSIGNMENT,
                        format!("cannot assign to `{name}`, which is not a variable"),
                    ),
                    None => {
                        self.undeclared(name, span);
                        return None;
                    }
                };
                self.checker.error(code, message, span);
                return None;
            }
        };
        let Local {
            mutable,
            declared_without_value,
            ..
        } = self.body.locals[local];
        if !mutable && !declared_without_value {
            self.checker.diagnostics.push(
                Diagnostic::new(
                    diagnostics::INVALID_ASSIGNMENT,
                    format!("cannot assign to immutable `{name}`"),
                )
                .label(&tree.file, span, "")
                .label(&tree.file, declared, "declared without `mut` here"),
            );
        }
        Some(local)
    }
}

#[cfg(test)]
mod tests {
    use model::DefId;
    use model::syntax;
    use source::SourceFile;

    use crate::testing::Reported;
    use crate::testing::reported;
    use crate::testing::with_checker;

    /// The THIR of every function body of `text`, as text, and the
    /// diagnostics.
    fn bodies(text: &str) -> (Vec<String>, Vec<Reported>) {
        let (bodies, types, reported) = with_checker(text, |checker| {
            let tree = &checker.module.syntax;
            let declarations: Vec<_> = tree.lists.items(tree.declarations).collect();
            let mut bodies = Vec::new();
            for declaration in declarations {
                let syntax::Decl::Function(function) = checker.module.syntax.decls[declaration]
                else {
                    continue;
                };
                let signature = checker.signature(declaration);
                bodies.extend(checker.body(&function, signature));
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
            let tree = &checker.module.syntax;
            let declaration = tree.lists.items(tree.declarations).next().unwrap();
            let syntax::Decl::Function(function) = tree.decls[declaration] else {
                unreachable!("the first declaration is a function")
            };
            let signature = checker.signature(declaration);
            checker.body(&function, signature);
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
