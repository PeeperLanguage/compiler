//! Checking a range value, `a..b`, into the core's `range[T]` constant or
//! range (`ExprKind::Range`).

use arena::Id;
use bigint::Limbs;
use check::Checked;
use check::builder::ThirBuilder;
use model::constant::Value;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use model::thir::ExprKind;
use source::Span;
use syntax::ast;
use syntax::ast::WrittenRange;

/// The range `start..end`, or `..=`, stepping by `start..end:step`, at
/// `span`: a `range[T]` of its ends' integer type `T`. A literal end
/// without a suffix takes the other end's type, or `T` when a
/// `range[T]` is `expected`; the step is of the range type's step type
/// and not a constant zero (`check_range_step`). A range without both
/// ends is not supported yet.
pub fn check_range<L: Checked>(
    builder: &mut ThirBuilder<'_, '_, L>,
    range: WrittenRange<L>,
    span: Span,
    expected: Option<TypeId>,
) -> Expr {
    let WrittenRange {
        inclusive,
        start: Some(start),
        end: Some(end),
        step,
    } = range
    else {
        builder
            .checker
            .not_supported(span, "ranges without both ends");
        return Expr::INVALID;
    };
    let hint = expected.and_then(|ty| match builder.checker.types[ty] {
        Type::Range { element, .. } => Some(element),
        _ => None,
    });
    let (start, end) = builder.check_range_ends(start, end, hint);
    let ty = builder
        .checker
        .range_type(builder.body.exprs[start].ty, span);
    let step_type = match builder.checker.types[ty] {
        Type::Range { step, .. } => step,
        _ => TypeId::INVALID,
    };
    let step = check_range_step(builder, step, step_type, span);
    if ty == TypeId::INVALID {
        return Expr::INVALID;
    }
    Expr {
        ty,
        kind: ExprKind::Range {
            start,
            end,
            step,
            inclusive,
        },
    }
}

/// The step of a range, written `step` or else 1 at `range_span`, of
/// the range type's step type `step_type` (`Types::range`), and not a
/// constant zero. With an invalid `step_type`, after an error in the
/// ends, it is checked only for its own errors.
fn check_range_step<L: Checked>(
    builder: &mut ThirBuilder<'_, '_, L>,
    step: Option<Id<ast::Expr<L>>>,
    step_type: TypeId,
    range_span: Span,
) -> Id<Expr> {
    let Some(step) = step else {
        let one = match builder.checker.types[step_type] {
            Type::Integer(unsigned) => Expr {
                ty: step_type,
                kind: ExprKind::Constant(Value::Integer(Limbs::new(unsigned.bits, &[1]))),
            },
            _ => Expr::INVALID,
        };
        return builder.body.add(one, range_span);
    };
    let step = match step_type {
        TypeId::INVALID => builder.add_expr(step, None),
        ty => builder.add_required(step, ty),
    };
    if let ExprKind::Constant(Value::Integer(limbs)) = &builder.body.exprs[step].kind
        && limbs.as_slice().iter().all(|&limb| limb == 0)
    {
        let span = builder.body.exprs.span(step);
        let message = "a range's step must not be zero";
        builder
            .checker
            .error(diagnostics::INVALID_OPERATION, message, span);
    }
    step
}
