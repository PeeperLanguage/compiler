//! Checking expressions into THIR. Each expression gets its type, and an
//! operation on values known at compile time is folded to its value.

use std::num::NonZeroU32;

use arena::Id;
use arena::List;
use bigint::Limbs;
use diagnostics::Diagnostic;
use diagnostics::Fix;
use diagnostics::suggest::nearest_name;
use ir::BinaryOp;
use ir::UnaryOp;
use ir::types::Float;
use ir::types::Type;
use ir::types::TypeId;
use ir::value;
use ir::value::Value;
use source::Span;
use token::TokenKind;

use crate::Constant;
use crate::Queries;
use crate::body::Expr;
use crate::body::ExprKind;
use crate::builtin::UNSUPPORTED_FUNCTIONS;
use crate::scope::Binding;
use crate::statement::BodyChecker;
use crate::statement::Named;

impl<Q: Queries> BodyChecker<'_, '_, Q> {
    /// `expression`, added to the body.
    pub(crate) fn value(
        &mut self,
        expression: Id<ast::Expr>,
        expected: Option<TypeId>,
    ) -> Id<Expr> {
        let checked = self.expr(expression, expected);
        self.body.add(checked, self.tree.exprs.span(expression))
    }

    /// `expression` as a value of the type `ty`, added to the body.
    pub(crate) fn expect(&mut self, expression: Id<ast::Expr>, ty: TypeId) -> Id<Expr> {
        let checked = self.coerced(expression, ty);
        self.body.add(checked, self.tree.exprs.span(expression))
    }

    /// `expression` as a value of the type `ty`, not yet added to the body;
    /// a mismatch is reported.
    pub(crate) fn coerced(&mut self, expression: Id<ast::Expr>, ty: TypeId) -> Expr {
        let span = self.tree.exprs.span(expression);
        let checked = self.expr(expression, Some(ty));
        self.coerce(checked, span, ty).unwrap_or_else(|mismatch| {
            self.checker.diagnostics.push(mismatch);
            Expr::INVALID
        })
    }

    /// `expr`, written at `span`, as a value of the type `to`: unchanged when
    /// it has that type, widened when its type widens to it
    /// (`Type::widens_to`), else the mismatch to report. An error already
    /// reported on either side gives an invalid expression and nothing to
    /// report.
    fn coerce(&mut self, expr: Expr, span: Span, to: TypeId) -> Result<Expr, Diagnostic> {
        if expr.ty == TypeId::INVALID || to == TypeId::INVALID {
            return Ok(Expr::INVALID);
        }
        if expr.ty == to {
            return Ok(expr);
        }
        let types = self.checker.queries.types();
        let (from_type, to_type) = (types[expr.ty].clone(), types[to].clone());
        if !from_type.widens_to(&to_type) {
            let (expected, found) = (types.text(to), types.text(expr.ty));
            return Err(Diagnostic::new(
                diagnostics::TYPE_MISMATCH,
                format!("mismatched types: expected `{expected}`, found `{found}`"),
            )
            .label(&self.tree.file, span, ""));
        }
        let kind = match expr.kind {
            ExprKind::Constant(constant) => {
                ExprKind::Constant(value::widen(&from_type, &to_type, &constant))
            }
            _ => ExprKind::Widen(self.body.add(expr, span)),
        };
        Ok(Expr { ty: to, kind })
    }

    /// The THIR of `expression`, not yet added to the body, so that a folded
    /// operation leaves no nodes for its operands. `expected` is the type the
    /// context expects, which a number literal without a suffix takes; where
    /// the context requires that type, its caller coerces the result to it.
    pub(crate) fn expr(&mut self, expression: Id<ast::Expr>, expected: Option<TypeId>) -> Expr {
        let tree = self.tree;
        let span = tree.exprs.span(expression);
        let what = match tree.exprs[expression] {
            ast::Expr::Name => return self.name(span),
            ast::Expr::Number => return self.number(span, None, expected),
            ast::Expr::Bool => {
                let value = Value::Bool(tree.file.slice(span) == "true");
                return Expr {
                    ty: TypeId::BOOL,
                    kind: ExprKind::Constant(value),
                };
            }
            ast::Expr::Parentheses(inner) => return self.expr(inner, expected),
            ast::Expr::Unary { op, value } => return self.unary(op, value, span, expected),
            ast::Expr::Binary { op, left, right } => {
                return self.binary(op, left, right, span, expected);
            }
            ast::Expr::Invalid => return Expr::INVALID, // the parser reported it
            ast::Expr::Path(path) => {
                return match self.path_binding(path) {
                    Some(binding) => self.global_value(binding, tree.file.slice(span), span),
                    None => Expr::INVALID,
                };
            }
            ast::Expr::Call { callee, arguments } => return self.call(callee, arguments, span),
            ast::Expr::String { .. } => "string literals",
            ast::Expr::Char { .. } => "character literals",
            ast::Expr::None => "`none` values",
            ast::Expr::Reference { .. } => "references",
            ast::Expr::RawAddress(_) => "raw addresses",
            ast::Expr::Free(_) => "`free` calls",
            ast::Expr::Print { .. } => "`print` calls",
            ast::Expr::Selector { .. } => "field accesses",
            ast::Expr::Index { .. } => "index expressions",
            ast::Expr::Range { .. } => "ranges",
            ast::Expr::StructLiteral { .. } => "struct literals",
            ast::Expr::VariantLiteral { .. } => "variant literals",
            ast::Expr::ArrayLiteral { .. } => "array literals",
            ast::Expr::Cast { .. } => "casts",
            ast::Expr::Is { .. } => "`is` tests",
            ast::Expr::OrElse { .. } => "`orelse` fallbacks",
            ast::Expr::Block(_) => "block expressions",
            ast::Expr::Pipe { .. } => "pipes",
        };
        self.checker.not_supported(span, what);
        Expr::INVALID
    }

    /// The name written at `span`, as a value.
    pub(crate) fn name(&mut self, span: Span) -> Expr {
        let name = self.tree.file.slice(span);
        if let Some((_, named)) = self.lookup(name) {
            return match named {
                Named::Local(local) => Expr {
                    ty: self.body.locals[*local].ty,
                    kind: ExprKind::Local(*local),
                },
                Named::Constant(constant) => constant_expr(constant.clone()),
            };
        }
        match self.checker.global(name) {
            Some(binding) => self.global_value(binding, name, span),
            None => self.undeclared(name, span),
        }
    }

    /// The module-level declaration `binding`, written as `text` at `span`,
    /// as a value.
    fn global_value(&mut self, binding: Binding, text: &str, span: Span) -> Expr {
        let (code, message) = match binding {
            Binding::Constant(constant) => {
                return constant_expr(self.checker.queries.constant(constant));
            }
            Binding::Function(_) => {
                self.checker.not_supported(span, "functions as values");
                return Expr::INVALID;
            }
            Binding::Type(_) => (
                diagnostics::INVALID_OPERATION,
                format!("`{text}` is a type, not a value"),
            ),
            Binding::Module(_) => (
                diagnostics::INVALID_OPERATION,
                format!("`{text}` is a module, not a value"),
            ),
        };
        self.checker.error(code, message, span);
        Expr::INVALID
    }

    /// Reports `name`, written at `span`, which nothing in scope declares:
    /// a built-in function, not supported yet, or an unknown name, with the
    /// nearest name that is declared.
    pub(crate) fn undeclared(&mut self, name: &str, span: Span) -> Expr {
        if UNSUPPORTED_FUNCTIONS.contains(&name) {
            self.checker.not_supported(span, "built-in functions");
            return Expr::INVALID;
        }
        // The module's names sorted, so the suggestion does not depend on the
        // hash order.
        let mut module_names: Vec<&str> = self.checker.scope.names().collect();
        module_names.sort_unstable();
        let local_names = self.names.iter().map(|&(local, ..)| local);
        let candidates = local_names.chain(module_names).map(|name| (name, 0));
        let file = &self.tree.file;
        let mut diagnostic = Diagnostic::new(
            diagnostics::UNDEFINED_NAME,
            format!("unknown name `{name}`"),
        )
        .label(file, span, "");
        if let Some(nearest) = nearest_name(name, candidates) {
            let fix = Fix {
                file: file.clone(),
                span,
                new_text: nearest.to_string(),
            };
            diagnostic = diagnostic.help(format!("did you mean `{nearest}`?"), vec![fix]);
        }
        self.checker.diagnostics.push(diagnostic);
        Expr::INVALID
    }

    /// What the path `module::name` written as the type expression `path`
    /// stands for, reporting a path that names nothing or is not supported.
    /// Another module's name is reachable only if it starts with an upper case
    /// letter, except in the prelude.
    fn path_binding(&mut self, path: Id<ast::Type>) -> Option<Binding> {
        let tree = self.tree;
        let span = tree.types.span(path);
        let segments: Vec<Id<ast::Type>> = match tree.types[path] {
            ast::Type::Path(segments) => tree.lists.items(segments).collect(),
            _ => {
                self.checker
                    .not_supported(span, "paths with type arguments");
                return None;
            }
        };
        let [module, member] = segments[..] else {
            self.checker
                .not_supported(span, "paths of more than two names");
            return None;
        };
        if !matches!(
            (tree.types[module], tree.types[member]),
            (ast::Type::Name, ast::Type::Name)
        ) {
            self.checker
                .not_supported(span, "paths with type arguments");
            return None;
        }
        let (module_span, member_span) = (tree.types.span(module), tree.types.span(member));
        let (module_name, member_name) =
            (tree.file.slice(module_span), tree.file.slice(member_span));
        let index = match self.checker.global(module_name) {
            Some(Binding::Module(index)) => index,
            Some(Binding::Type(_)) => {
                self.checker.not_supported(span, "paths into types");
                return None;
            }
            Some(Binding::Function(_) | Binding::Constant(_)) => {
                let message = format!("`{module_name}` is not a module");
                self.checker
                    .error(diagnostics::INVALID_OPERATION, message, module_span);
                return None;
            }
            None => {
                self.undeclared(module_name, module_span);
                return None;
            }
        };
        let Some(binding) = self.checker.scopes[index as usize].get(member_name) else {
            let message = format!("unknown name `{member_name}` in module `{module_name}`");
            self.checker
                .error(diagnostics::UNDEFINED_NAME, message, member_span);
            return None;
        };
        // The prelude's names are in every module's scope, private or not.
        let exported =
            self.checker.prelude == Some(index) || member_name.starts_with(char::is_uppercase);
        if !exported {
            let file = &tree.file;
            self.checker.diagnostics.push(
                Diagnostic::new(
                    diagnostics::NOT_EXPORTED,
                    format!("`{member_name}` is not exported from `{module_name}`"),
                )
                .label(file, member_span, "")
                .note(
                    "names that start with an upper case letter are exported; others are private",
                ),
            );
            return None;
        }
        Some(binding)
    }

    /// `callee(arguments)`, written at `span`. Only a module's function, by
    /// its name or its path, can be called.
    fn call(&mut self, callee: Id<ast::Expr>, arguments: List<ast::Expr>, span: Span) -> Expr {
        let tree = self.tree;
        let callee_span = tree.exprs.span(callee);
        let text = tree.file.slice(callee_span);
        let binding = match tree.exprs[callee] {
            ast::Expr::Name => match self.lookup(text) {
                // Their errors were reported.
                Some((_, Named::Constant(None))) => None,
                Some((_, &Named::Local(local)))
                    if self.body.locals[local].ty == TypeId::INVALID =>
                {
                    None
                }
                Some(_) => Some(Err(())),
                None => match self.checker.global(text) {
                    Some(binding) => Some(Ok(binding)),
                    None => {
                        self.undeclared(text, callee_span);
                        None
                    }
                },
            },
            ast::Expr::Path(path) => self.path_binding(path).map(Ok),
            ast::Expr::Selector { .. } => {
                self.checker.not_supported(callee_span, "method calls");
                None
            }
            _ => {
                self.checker
                    .not_supported(callee_span, "calls of computed functions");
                None
            }
        };
        let function = match binding {
            Some(Ok(Binding::Function(function))) => Some(function),
            Some(_) => {
                let message = format!("`{text}` is not a function");
                self.checker
                    .error(diagnostics::NOT_CALLABLE, message, callee_span);
                None
            }
            None => None,
        };
        let signature = function.map(|function| {
            let ty = self.checker.queries.signature(function);
            (function, self.checker.queries.types()[ty].clone())
        });
        let Some((function, Type::Function { parameters, result })) = signature else {
            for argument in tree.lists.items(arguments) {
                self.value(argument, None); // for its own errors
            }
            return Expr::INVALID;
        };
        let arity_matches = parameters.len() == arguments.len();
        if !arity_matches {
            let message = format!(
                "`{text}` takes {} argument{}, but {} {} given",
                parameters.len(),
                if parameters.len() == 1 { "" } else { "s" },
                arguments.len(),
                if arguments.len() == 1 { "was" } else { "were" },
            );
            self.checker
                .error(diagnostics::WRONG_ARGUMENT_COUNT, message, span);
        }
        let mut checked: Vec<NonZeroU32> = Vec::with_capacity(arguments.len());
        for (index, argument) in tree.lists.items(arguments).enumerate() {
            let argument = match parameters.get(index) {
                Some(&parameter) => self.expect(argument, parameter),
                None => self.value(argument, None),
            };
            checked.push(argument.into());
        }
        if !arity_matches {
            return Expr::INVALID;
        }
        Expr {
            ty: result,
            kind: ExprKind::Call {
                function,
                arguments: self.body.lists.add(&checked),
            },
        }
    }

    /// The number literal at `number`, or its negation written at `negated`
    /// (`-128`, which fits `i8` where `128` does not). It takes its suffix's
    /// type, else the type `expected` if it can (`takes_type`), else `i32` or
    /// `f64`.
    fn number(&mut self, number: Span, negated: Option<Span>, expected: Option<TypeId>) -> Expr {
        let tree = self.tree;
        let literal = numeric::split(tree.file.slice(number));
        let pointer_bits = self.checker.queries.pointer_bits();
        let types = self.checker.queries.types();
        let ty = if literal.suffix.is_empty() {
            match expected {
                Some(ty) if takes_type(&literal, &types[ty]) => ty,
                _ if literal.is_float() => TypeId::F64,
                _ => TypeId::I32,
            }
        } else if let Some(ty) = types.builtin(literal.suffix, pointer_bits) {
            ty
        } else {
            let message = format!("unsupported numeric literal suffix `{}`", literal.suffix);
            self.checker
                .error(diagnostics::INVALID_NUMBER_SUFFIX, message, number);
            return Expr::INVALID;
        };
        let negative = negated.is_some();
        let value = match types[ty] {
            Type::Integer(integer) => {
                let magnitude = literal.integer_value();
                integer.fits(&magnitude, negative).then(|| {
                    let value = Limbs::new(integer.bits, &magnitude);
                    Value::Integer(if negative {
                        bigint::negate(integer.bits, &value)
                    } else {
                        value
                    })
                })
            }
            Type::Float(float) => {
                let value = match float {
                    Float::F32 => literal.float_value::<f32>().map(f64::from),
                    Float::F64 => literal.float_value::<f64>(),
                };
                value.map(|value| Value::Float(if negative { -value } else { value }.to_bits()))
            }
            _ => unreachable!("a number literal's type is a number type"),
        };
        let Some(value) = value else {
            let written = negated.unwrap_or(number);
            let message = format!(
                "literal `{}` does not fit `{}`",
                tree.file.slice(written),
                types.text(ty)
            );
            self.checker
                .error(diagnostics::INVALID_NUMBER, message, written);
            return Expr::INVALID;
        };
        Expr {
            ty,
            kind: ExprKind::Constant(value),
        }
    }

    /// `op operand`, written at `span`.
    fn unary(
        &mut self,
        op: TokenKind,
        operand: Id<ast::Expr>,
        span: Span,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = self.tree;
        let operand_span = tree.exprs.span(operand);
        // Only a literal right after `-` is a negative literal: `-(128)`
        // negates `128`, which does not fit `i8`.
        if op == TokenKind::Minus && matches!(tree.exprs[operand], ast::Expr::Number) {
            return self.number(operand_span, Some(span), expected);
        }
        let checked = match op {
            TokenKind::Bang => self.expr(operand, Some(TypeId::BOOL)),
            _ => self.expr(operand, expected),
        };
        if checked.ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let ty = self.checker.queries.types()[checked.ty].clone();
        let number = matches!(ty, Type::Integer(_) | Type::Float(_));
        let (unary_op, accepted) = match op {
            TokenKind::Plus => (None, number),
            TokenKind::Minus => (Some(UnaryOp::Negate), number),
            TokenKind::Bang => (Some(UnaryOp::LogicalNot), ty == Type::Bool),
            TokenKind::Tilde => (Some(UnaryOp::BitNot), matches!(ty, Type::Integer(_))),
            _ => unreachable!("`{}` is not a unary operator", op.name()),
        };
        if !accepted {
            return self.invalid_operand(op, checked.ty, span);
        }
        let Some(unary_op) = unary_op else {
            return checked; // `+x` is `x`
        };
        let result = checked.ty;
        let kind = match checked.kind {
            ExprKind::Constant(constant) => ExprKind::Constant(
                value::unary(unary_op, &ty, &constant).expect("unary operators do not trap"),
            ),
            kind => ExprKind::Unary {
                op: unary_op,
                value: self.body.add(Expr { ty: result, kind }, operand_span),
            },
        };
        Expr { ty: result, kind }
    }

    /// `left op right`, written at `span`. The operation has the left
    /// operand's type, and the right operand is converted to it, except that a
    /// shift count keeps its own integer type. The left operand takes the type
    /// the whole expression is expected to have, unless the operator gives a
    /// `bool` from other types (`==`, `<`), and the right operand takes the
    /// left one's.
    fn binary(
        &mut self,
        op: TokenKind,
        left: Id<ast::Expr>,
        right: Id<ast::Expr>,
        span: Span,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = self.tree;
        let (left_span, right_span) = (tree.exprs.span(left), tree.exprs.span(right));
        let binary_op = binary_op(op);
        let comparison = matches!(
            binary_op,
            BinaryOp::EqualTo
                | BinaryOp::NotEqualTo
                | BinaryOp::LessThan
                | BinaryOp::LessOrEqual
                | BinaryOp::GreaterThan
                | BinaryOp::GreaterOrEqual
        );
        let shift = matches!(binary_op, BinaryOp::ShiftLeft | BinaryOp::ShiftRight);
        let left_literal = unsuffixed_literal(tree, left);
        let left = self.expr(left, if comparison { None } else { expected });
        let right = if shift {
            self.expr(right, None)
        } else {
            let checked = self.expr(right, Some(left.ty));
            let found = checked.ty;
            match self.coerce(checked, right_span, left.ty) {
                Ok(right) => right,
                Err(mut mismatch) => {
                    let types = self.checker.queries.types();
                    let fixable = left_literal
                        .filter(|&literal| takes_type(&numeric::split(literal), &types[found]));
                    if let Some(literal) = fixable {
                        let found = types.text(found);
                        let help =
                            format!("write `{literal}{found}` to give the left literal that type");
                        mismatch = mismatch.help(help, Vec::new());
                    }
                    self.checker.diagnostics.push(mismatch);
                    Expr::INVALID
                }
            }
        };
        if left.ty == TypeId::INVALID || right.ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let types = self.checker.queries.types();
        let (left_type, right_type) = (types[left.ty].clone(), types[right.ty].clone());
        let accepted = match binary_op {
            BinaryOp::Add
            | BinaryOp::Subtract
            | BinaryOp::Multiply
            | BinaryOp::Divide
            | BinaryOp::Remainder
            | BinaryOp::LessThan
            | BinaryOp::LessOrEqual
            | BinaryOp::GreaterThan
            | BinaryOp::GreaterOrEqual => matches!(left_type, Type::Integer(_) | Type::Float(_)),
            BinaryOp::ShiftLeft
            | BinaryOp::ShiftRight
            | BinaryOp::BitAnd
            | BinaryOp::BitOr
            | BinaryOp::BitXor => matches!(left_type, Type::Integer(_)),
            BinaryOp::EqualTo | BinaryOp::NotEqualTo => {
                matches!(left_type, Type::Integer(_) | Type::Float(_) | Type::Bool)
            }
            BinaryOp::LogicalAnd | BinaryOp::LogicalOr => left_type == Type::Bool,
        };
        if !accepted {
            return self.invalid_operand(op, left.ty, span);
        }
        if shift && !matches!(right_type, Type::Integer(_)) {
            let count = types.text(right.ty);
            let message = format!("a shift count must be an integer, found `{count}`");
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, right_span);
            return Expr::INVALID;
        }
        if let Some(message) = known_trap(binary_op, &left_type, &right_type, &right) {
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, right_span);
            return Expr::INVALID;
        }
        let ty = if comparison { TypeId::BOOL } else { left.ty };
        if let (ExprKind::Constant(left_value), ExprKind::Constant(right_value)) =
            (&left.kind, &right.kind)
        {
            let folded = value::binary(binary_op, &left_type, left_value, &right_type, right_value)
                .expect("known traps were reported before folding");
            return Expr {
                ty,
                kind: ExprKind::Constant(folded),
            };
        }
        let left = self.body.add(left, left_span);
        let right = self.body.add(right, right_span);
        Expr {
            ty,
            kind: ExprKind::Binary {
                op: binary_op,
                left,
                right,
            },
        }
    }

    /// Reports `op` applied to an operand of the type `ty`, at `span`.
    fn invalid_operand(&mut self, op: TokenKind, ty: TypeId, span: Span) -> Expr {
        let ty = self.checker.queries.types().text(ty);
        let message = format!("operator `{}` cannot be applied to `{ty}`", op.name());
        self.checker
            .error(diagnostics::INVALID_OPERATION, message, span);
        Expr::INVALID
    }
}

/// The value of a constant (`Queries::constant`) as an expression.
fn constant_expr(constant: Option<Constant>) -> Expr {
    match constant {
        Some((ty, value)) => Expr {
            ty,
            kind: ExprKind::Constant(value),
        },
        None => Expr::INVALID,
    }
}

/// Why `op` on a left operand of `left_type` and `right` of `right_type`
/// traps whatever the left value is: a division by a constant zero, or a
/// shift by a constant count out of range.
fn known_trap(op: BinaryOp, left_type: &Type, right_type: &Type, right: &Expr) -> Option<String> {
    let (Type::Integer(shifted), Type::Integer(count_type)) = (left_type, right_type) else {
        return None; // float division gives an infinity or NaN
    };
    let ExprKind::Constant(Value::Integer(right)) = &right.kind else {
        return None;
    };
    match op {
        BinaryOp::Divide | BinaryOp::Remainder
            if right.as_slice().iter().all(|&limb| limb == 0) =>
        {
            Some("division by zero".to_string())
        }
        BinaryOp::ShiftLeft | BinaryOp::ShiftRight => {
            value::shift_count(*shifted, *count_type, right)
                .is_err()
                .then(|| format!("a shift count must be from 0 to {}", shifted.bits - 1))
        }
        _ => None,
    }
}

/// Whether a number literal without a suffix can have the type `ty`: an
/// integer literal any number type, a decimal float literal a float type.
/// Hexadecimal, octal and binary literals are never floats.
fn takes_type(literal: &numeric::Literal, ty: &Type) -> bool {
    match ty {
        Type::Integer(_) => !literal.is_float(),
        Type::Float(_) => literal.base == 10,
        _ => false,
    }
}

/// The text of `expression` if it is a number literal without a suffix.
fn unsuffixed_literal(tree: &ast::Tree, expression: Id<ast::Expr>) -> Option<&str> {
    let text = tree.file.slice(tree.exprs.span(expression));
    let number = matches!(tree.exprs[expression], ast::Expr::Number);
    (number && numeric::split(text).suffix.is_empty()).then_some(text)
}

/// The operator of a binary expression.
fn binary_op(op: TokenKind) -> BinaryOp {
    match op {
        TokenKind::Plus => BinaryOp::Add,
        TokenKind::Minus => BinaryOp::Subtract,
        TokenKind::Asterisk => BinaryOp::Multiply,
        TokenKind::Slash => BinaryOp::Divide,
        TokenKind::Percent => BinaryOp::Remainder,
        TokenKind::ShiftLeft => BinaryOp::ShiftLeft,
        TokenKind::ShiftRight => BinaryOp::ShiftRight,
        TokenKind::Amp => BinaryOp::BitAnd,
        TokenKind::Bar => BinaryOp::BitOr,
        TokenKind::Caret => BinaryOp::BitXor,
        TokenKind::EqualEqual => BinaryOp::EqualTo,
        TokenKind::BangEqual => BinaryOp::NotEqualTo,
        TokenKind::Less => BinaryOp::LessThan,
        TokenKind::LessEqual => BinaryOp::LessOrEqual,
        TokenKind::Greater => BinaryOp::GreaterThan,
        TokenKind::GreaterEqual => BinaryOp::GreaterOrEqual,
        TokenKind::AndAnd => BinaryOp::LogicalAnd,
        TokenKind::OrOr => BinaryOp::LogicalOr,
        _ => unreachable!("`{}` is not a binary operator", op.name()),
    }
}
