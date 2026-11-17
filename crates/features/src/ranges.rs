//! Ranges: `a..b`, `a..=b`, `a..b:step`, and `..b` or `a..` before a `]`.
//! The written parts are the core's `WrittenRange`, as slicing reads them
//! (`Language::written_range`); this file reads them and checks a range
//! value into the core's `range[T]`, so the phases after it know nothing of
//! them.

use arena::Id;
use bigint::Limbs;
use check::builder::ThirBuilder;
use diagnostics::Diagnostic;
use model::constant::Value;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::ast::WrittenRange;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::ExprNode;
use crate::FeatureExpr;
use crate::Peeper;

/// A range, as written: its parts are the core's, which slicing and loops
/// read (`Language::written_range`).
#[derive(Clone, Copy, Debug)]
pub struct Range {
    pub written: WrittenRange<Peeper>,
}

impl Range {
    /// Adds the rules that read ranges to `rules`.
    pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
        rules.prefix(TokenKind::DotDot, open_range);
        rules.prefix(TokenKind::DotDotEqual, open_range);
        rules.infix(TokenKind::DotDot, Precedence::Range, range);
        rules.infix(TokenKind::DotDotEqual, Precedence::Range, range);
    }
}

/// Reads a range with no start: `..b`, `..=b`, or `..` before `]`.
fn open_range(p: &mut Parser<Peeper>) -> Id<ast::Expr<Peeper>> {
    let start = p.tok().span.start;
    range_from(p, start, None)
}

/// Reads a range from `low`: `a..b`, `a..=b`, or `a..` before `]`.
fn range(p: &mut Parser<Peeper>, low: Id<ast::Expr<Peeper>>) -> Id<ast::Expr<Peeper>> {
    range_from(p, p.start_of(low), Some(low))
}

/// Reads `..` or `..=` and what follows it: an end, then `:step` to step by
/// more than one. Only right before the `]` may a range leave out its end.
fn range_from(
    p: &mut Parser<Peeper>,
    start: u32,
    low: Option<Id<ast::Expr<Peeper>>>,
) -> Id<ast::Expr<Peeper>> {
    let op = p.next();
    let mut high = None;
    if !p.at(TokenKind::RightBracket) {
        high = Some(p.expr(Precedence::Range));
    } else if op.kind == TokenKind::DotDotEqual {
        p.repair(
            Diagnostic::new(
                diagnostics::INVALID_EXPRESSION,
                "inclusive range requires an end bound",
            )
            .label(
                &p.tree.file,
                op.span,
                "add the inclusive end bound after `..=`",
            ),
        );
    }
    let mut step = None;
    if high.is_some() && p.at(TokenKind::Colon) {
        p.next();
        step = Some(p.expr(Precedence::Range));
    }
    let inclusive = op.kind == TokenKind::DotDotEqual;
    let written = WrittenRange {
        inclusive,
        start: low,
        end: high,
        step,
    };
    p.add(
        start,
        ast::Expr::Feature(FeatureExpr::Range(Range { written })),
    )
}

impl ExprNode for Range {
    fn precedence(&self) -> Precedence {
        Precedence::Range
    }

    fn written_range(&self) -> Option<WrittenRange<Peeper>> {
        Some(self.written)
    }

    /// The range `start..end`, or `..=`, stepping by `start..end:step`, at
    /// `span`: a `range[T]` of its ends' integer type `T`. A literal end
    /// without a suffix takes the other end's type, or `T` when a
    /// `range[T]` is `expected`; the step is of the range type's step type
    /// and not a constant zero (`check_range_step`). A range without both
    /// ends is not supported yet.
    fn check(
        &self,
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        span: Span,
        expected: Option<TypeId>,
    ) -> thir::Expr {
        let WrittenRange {
            inclusive,
            start: Some(start),
            end: Some(end),
            step,
        } = self.written
        else {
            builder
                .checker
                .not_supported(span, "ranges without both ends");
            return thir::Expr::INVALID;
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
}

/// The step of a range, written `step` or else 1 at `range_span`, of
/// the range type's step type `step_type` (`Types::range`), and not a
/// constant zero. With an invalid `step_type`, after an error in the
/// ends, it is checked only for its own errors.
fn check_range_step(
    builder: &mut ThirBuilder<'_, '_, Peeper>,
    step: Option<Id<ast::Expr<Peeper>>>,
    step_type: TypeId,
    range_span: Span,
) -> Id<thir::Expr> {
    let Some(step) = step else {
        let one = match builder.checker.types[step_type] {
            Type::Integer(unsigned) => thir::Expr {
                ty: step_type,
                kind: thir::ExprKind::Constant(Value::Integer(Limbs::new(unsigned.bits, &[1]))),
            },
            _ => thir::Expr::INVALID,
        };
        return builder.body.add(one, range_span);
    };
    let step = match step_type {
        TypeId::INVALID => builder.add_expr(step, None),
        ty => builder.add_required(step, ty),
    };
    if let thir::ExprKind::Constant(Value::Integer(limbs)) = &builder.body.exprs[step].kind
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

#[cfg(test)]
mod tests;
