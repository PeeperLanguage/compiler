//! Ranges: the checker's file for `a..b`, `a..=b`, `a..b:step`, and `..b` or
//! `a..` before a `]`, as the parser reads them. A range value is checked into
//! the built-in type `range[T]`, so the phases after it know nothing of this
//! file.

use crate::builder;
use arena::Id;
use bigint::Limbs;
use model::constant;
use model::semantic_types;
use model::thir;
use source::Span;
use tree::ast;

/// The step of a range, written `step` or else 1 at `range_span`, of
/// the range type's step type `step_type` (`Types::range`), and not a
/// constant zero. With an invalid `step_type`, after an error in the
/// ends, it is checked only for its own errors.
fn check_range_step(
    builder: &mut builder::ThirBuilder<'_, '_>,
    step: Option<Id<ast::Expr>>,
    step_type: semantic_types::TypeId,
    range_span: Span,
) -> Id<thir::Expr> {
    let Some(step) = step else {
        let one = match builder.checker.types[step_type] {
            semantic_types::Type::Integer(unsigned) => thir::Expr {
                ty: step_type,
                kind: thir::ExprKind::Constant(constant::Value::Integer(Limbs::new(
                    unsigned.bits,
                    &[1],
                ))),
            },
            _ => thir::Expr::INVALID,
        };
        return builder.body.add(one, range_span);
    };
    let step = match step_type {
        semantic_types::TypeId::INVALID => builder.add_expr(step, None),
        ty => builder.add_required(step, ty),
    };
    if let thir::ExprKind::Constant(constant::Value::Integer(limbs)) =
        &builder.body.exprs[step].kind
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
/// The range `start..end`, or `..=`, stepping by `start..end:step`, at
/// `span`: a `range[T]` of its ends' integer type `T`. A literal end
/// without a suffix takes the other end's type, or `T` when a
/// `range[T]` is `expected`; the step is of the range type's step type
/// and not a constant zero (`check_range_step`). A range without both
/// ends is not supported yet.
pub(crate) fn check_range(
    builder: &mut builder::ThirBuilder<'_, '_>,
    written: ast::WrittenRange,
    span: Span,
    expected: Option<semantic_types::TypeId>,
) -> thir::Expr {
    let ast::WrittenRange {
        inclusive,
        start: Some(start),
        end: Some(end),
        step,
    } = written
    else {
        builder
            .checker
            .not_supported(span, "ranges without both ends");
        return thir::Expr::INVALID;
    };
    let hint = expected.and_then(|ty| match builder.checker.types[ty] {
        semantic_types::Type::Range { element, .. } => Some(element),
        _ => None,
    });
    let (start, end) = builder.check_range_ends(start, end, hint);
    let ty = builder
        .checker
        .range_type(builder.body.exprs[start].ty, span);
    let step_type = match builder.checker.types[ty] {
        semantic_types::Type::Range { step, .. } => step,
        _ => semantic_types::TypeId::INVALID,
    };
    let step = check_range_step(builder, step, step_type, span);
    if ty == semantic_types::TypeId::INVALID {
        return thir::Expr::INVALID;
    }
    thir::Expr {
        ty,
        kind: thir::ExprKind::Range {
            start,
            end,
            step,
            inclusive,
        },
    }
}

#[cfg(test)]
mod tests {
    //! Checking ranges: a loop over a range takes its ends' type, and its step.

    use crate::testing;

    /// A range's literal start takes the end's type; its variables are
    /// declared in the body. Its step is the ends' type made unsigned, 1
    /// when not written.
    #[test]
    fn loops() {
        let (bodies, reported) = testing::bodies(
            "fn f(n: i64) {
                for i in 0..n { let j = i; }
                for 0..3 { }
                for k in 1..=n { }
                for i, m in n..0:2 { }
            }",
        );
        assert!(reported.is_empty(), "{reported:?}");
        assert_eq!(
            bodies,
            ["    for i.1 in 0:i64..n.0 by 1:u64 {
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
}
