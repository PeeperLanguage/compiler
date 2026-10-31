//! Checking operators: number literals, `as`, unary and binary operators,
//! folded when their operands are constants.

use arena::Id;
use bigint::Limbs;
use model::ast;
use model::constant::Value;
use model::operators::BinaryOp;
use model::operators::UnaryOp;
use model::semantic_types::Conversion;
use model::semantic_types::Float;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use model::thir::FactKind;
use source::Span;

use crate::builder::ThirBuilder;
use crate::expression::TypeExpectation;
use crate::expression::unsuffixed_literal;

impl ThirBuilder<'_, '_> {
    /// `value as ty`: `value` as a value of the type `ty`, which its type
    /// converts to with `as` or without it (`Types::conversion`). A number
    /// literal takes the type `ty` if it can, so `300 as u8` is an error.
    pub(super) fn check_cast(
        &mut self,
        value: Id<ast::Expr>,
        ty: Id<ast::Type>,
        span: Span,
    ) -> Expr {
        let to = self.checker.resolve_value_type(ty);
        let checked = self.check_expr(value, TypeExpectation::Hint(Some(to)));
        if checked.ty == TypeId::INVALID || to == TypeId::INVALID {
            return Expr::INVALID;
        }
        let value_span = self.checker.module().ast.exprs.span(value);
        let found = checked.ty;
        let conversion = self.checker.types.conversion(found, to);
        if let Some(converted) = self.convert(checked, value_span, to, conversion) {
            return converted;
        }
        let (found_text, to_text) = self.checker.type_texts(found, to);
        let message = format!("`{found_text}` cannot be converted to `{to_text}`");
        let types = &self.checker.types;
        let diagnostic = self.type_error(
            diagnostics::INVALID_CAST,
            message,
            span,
            value,
            found,
            |ty| types.conversion(ty, to) != Conversion::Never,
        );
        self.checker.report(diagnostic);
        Expr::INVALID
    }

    /// The number literal at `number`, or its negation written at `negated`
    /// (`-128`, which fits `i8` where `128` does not). It takes its suffix's
    /// type, else the type `expected` if it can (`takes_type`), else `i32` or
    /// `f64`. Its type is recorded even when its value does not fit.
    pub(super) fn check_number(
        &mut self,
        number: Span,
        negated: Option<Span>,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let literal = literal::numeric::split(tree.file.slice(number));
        let pointer_bits = self.checker.target.pointer_bits;
        let types = &mut self.checker.types;
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
        self.checker.record(number, FactKind::Literal(ty));
        let negative = negated.is_some();
        let value = match self.checker.types[ty] {
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
                self.checker.type_text(ty)
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
    pub(super) fn check_unary(
        &mut self,
        op: UnaryOp,
        operand: Id<ast::Expr>,
        span: Span,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let operand_span = tree.exprs.span(operand);
        // Only a literal right after `-` is a negative literal: `-(128)`
        // negates `128`, which does not fit `i8`.
        if op == UnaryOp::Negate && matches!(tree.exprs[operand], ast::Expr::Number) {
            return self.check_number(operand_span, Some(span), expected);
        }
        let checked = match op {
            UnaryOp::LogicalNot => {
                self.check_expr(operand, TypeExpectation::Hint(Some(TypeId::BOOL)))
            }
            _ => self.check_expr(operand, TypeExpectation::Hint(expected)),
        };
        if checked.ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        let ty = self.checker.types[checked.ty].clone();
        let fits = |ty: &Type| match op {
            UnaryOp::Negate => ty.is_number(),
            UnaryOp::LogicalNot => *ty == Type::Bool,
            UnaryOp::BitNot => matches!(ty, Type::Integer(_)),
        };
        if !fits(&ty) {
            return self.report_invalid_operand(op.symbol(), checked.ty, operand, span, fits);
        }
        let result = checked.ty;
        let kind = match checked.kind {
            ExprKind::Constant(constant) => ExprKind::Constant(
                eval::operators::unary(op, &ty, &constant).expect("unary operators do not trap"),
            ),
            kind => ExprKind::Unary {
                op,
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
    pub(super) fn check_binary(
        &mut self,
        op: BinaryOp,
        left_operand: Id<ast::Expr>,
        right_operand: Id<ast::Expr>,
        span: Span,
        expected: Option<TypeId>,
    ) -> Expr {
        let tree = &self.checker.module().ast;
        let (left_span, right_span) = (
            tree.exprs.span(left_operand),
            tree.exprs.span(right_operand),
        );
        let comparison = matches!(
            op,
            BinaryOp::EqualTo
                | BinaryOp::NotEqualTo
                | BinaryOp::LessThan
                | BinaryOp::LessOrEqual
                | BinaryOp::GreaterThan
                | BinaryOp::GreaterOrEqual
        );
        // `none` takes its type from the other operand, so `none == p` is
        // checked as `p == none`: the same result, and `none` does nothing.
        if matches!(op, BinaryOp::EqualTo | BinaryOp::NotEqualTo)
            && matches!(tree.exprs[left_operand], ast::Expr::None)
            && !matches!(tree.exprs[right_operand], ast::Expr::None)
        {
            return self.check_binary(op, right_operand, left_operand, span, expected);
        }
        let shift = matches!(op, BinaryOp::ShiftLeft | BinaryOp::ShiftRight);
        let left_literal = unsuffixed_literal(tree, left_operand);
        let left = self.check_expr(
            left_operand,
            TypeExpectation::Hint(if comparison { None } else { expected }),
        );
        let left_type = self.checker.types[left.ty].clone();
        let fits = |ty: &Type| match op {
            BinaryOp::Add
            | BinaryOp::Subtract
            | BinaryOp::Multiply
            | BinaryOp::Divide
            | BinaryOp::Remainder
            | BinaryOp::LessThan
            | BinaryOp::LessOrEqual
            | BinaryOp::GreaterThan
            | BinaryOp::GreaterOrEqual => ty.is_number(),
            BinaryOp::ShiftLeft
            | BinaryOp::ShiftRight
            | BinaryOp::BitAnd
            | BinaryOp::BitOr
            | BinaryOp::BitXor => matches!(ty, Type::Integer(_)),
            BinaryOp::EqualTo | BinaryOp::NotEqualTo => {
                ty.is_number() || matches!(ty, Type::Bool | Type::RawPointer { .. })
            }
            BinaryOp::LogicalAnd | BinaryOp::LogicalOr => *ty == Type::Bool,
        };
        let left_fits = fits(&left_type);
        // In a constant's value, `false && x` and `true || x` are the left
        // operand: `x` is checked but, as when the program runs, not run.
        let skips_right = self.in_constant
            && matches!(
                (op, &left.kind),
                (BinaryOp::LogicalAnd, ExprKind::Constant(Value::Bool(false)))
                    | (BinaryOp::LogicalOr, ExprKind::Constant(Value::Bool(true)))
            );
        let in_constant = self.in_constant;
        self.in_constant = in_constant && !skips_right;
        // Matching the right operand to a left one the operator rejects would
        // blame the right one.
        let right = if shift || !left_fits {
            self.check_expr(right_operand, TypeExpectation::Hint(None))
        } else {
            let checked = self.check_expr(right_operand, TypeExpectation::Hint(Some(left.ty)));
            let found = checked.ty;
            match self.coerce(checked, right_operand, left.ty) {
                Ok(right) => right,
                Err(mut mismatch) => {
                    let checker = &self.checker;
                    let fixable = left_literal.filter(|&literal| {
                        takes_type(&literal::numeric::split(literal), &checker.types[found])
                    });
                    if let Some(literal) = fixable {
                        // The suffix names the type itself, not an alias.
                        let found = checker.type_text(checker.types.real(found));
                        let help =
                            format!("write `{literal}{found}` to give the left literal that type");
                        mismatch = mismatch.help(help, Vec::new());
                    } else {
                        mismatch = self.help_cast(mismatch, right_operand, found, left.ty);
                    }
                    self.checker.report(mismatch);
                    Expr::INVALID
                }
            }
        };
        self.in_constant = in_constant;
        if left.ty == TypeId::INVALID || right.ty == TypeId::INVALID {
            return Expr::INVALID;
        }
        if !left_fits {
            return self.report_invalid_operand(op.symbol(), left.ty, left_operand, span, fits);
        }
        let right_type = self.checker.types[right.ty].clone();
        if shift && !matches!(right_type, Type::Integer(_)) {
            let count = self.checker.type_text(right.ty);
            let message = format!("a shift count must be an integer, found `{count}`");
            let types = &self.checker.types;
            let diagnostic = self.type_error(
                diagnostics::INVALID_OPERATION,
                message,
                right_span,
                right_operand,
                right.ty,
                |ty| matches!(types[ty], Type::Integer(_)),
            );
            self.checker.report(diagnostic);
            return Expr::INVALID;
        }
        if let ExprKind::Constant(ref value) = right.kind
            && let Some(trap) = eval::operators::known_trap(op, &left_type, &right_type, value)
        {
            let message = eval::messages::trap_message(trap, &left_type);
            self.checker
                .error(diagnostics::INVALID_OPERATION, message, right_span);
            return Expr::INVALID;
        }
        if skips_right {
            return left;
        }
        let ty = if comparison { TypeId::BOOL } else { left.ty };
        if let (ExprKind::Constant(left_value), ExprKind::Constant(right_value)) =
            (&left.kind, &right.kind)
        {
            let folded =
                eval::operators::binary(op, &left_type, left_value, &right_type, right_value)
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
            kind: ExprKind::Binary { op, left, right },
        }
    }

    /// Reports `op` in the expression at `span` applied to `operand` of the
    /// type `ty`, which `fits` rejects.
    pub(super) fn report_invalid_operand(
        &mut self,
        op: &str,
        ty: TypeId,
        operand: Id<ast::Expr>,
        span: Span,
        fits: impl Fn(&Type) -> bool,
    ) -> Expr {
        let message = format!(
            "operator `{op}` cannot be applied to `{}`",
            self.checker.type_text(ty)
        );
        let types = &self.checker.types;
        let diagnostic = self.type_error(
            diagnostics::INVALID_OPERATION,
            message,
            span,
            operand,
            ty,
            |ty| fits(&types[ty]),
        );
        self.checker.report(diagnostic);
        Expr::INVALID
    }
}

/// Whether a number literal without a suffix can have the type `ty`: an
/// integer literal any number type, a decimal float literal a float type.
/// Hexadecimal, octal and binary literals are never floats.
fn takes_type(literal: &literal::numeric::Literal, ty: &Type) -> bool {
    match ty {
        Type::Integer(_) => !literal.is_float(),
        Type::Float(_) => literal.base == 10,
        _ => false,
    }
}
