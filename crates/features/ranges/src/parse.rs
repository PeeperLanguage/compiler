//! Reading ranges: `a..b`, `a..=b`, `a..b:step`, and an open end before
//! `]`.

use arena::Id;
use diagnostics::Diagnostic;
use parser::Grammar;
use parser::Parser;
use parser::Rules;
use syntax::ast::Expr;
use syntax::ast::WrittenRange;
use syntax::operators::Precedence;
use token::TokenKind;

/// Adds the rules that read ranges to `rules`.
pub const fn rows<L>(rules: &mut Rules<L>)
where
    L: Grammar<Expr: From<WrittenRange<L>>>,
{
    rules.prefix(TokenKind::DotDot, open_range);
    rules.prefix(TokenKind::DotDotEqual, open_range);
    rules.infix(TokenKind::DotDot, Precedence::Range, range);
    rules.infix(TokenKind::DotDotEqual, Precedence::Range, range);
}

/// Reads a range with no start: `..b`, `..=b`, or `..` before `]`.
fn open_range<L>(p: &mut Parser<L>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<WrittenRange<L>>>,
{
    let start = p.tok().span.start;
    range_from(p, start, None)
}

/// Reads a range from `low`: `a..b`, `a..=b`, or `a..` before `]`.
fn range<L>(p: &mut Parser<L>, low: Id<Expr<L>>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<WrittenRange<L>>>,
{
    range_from(p, p.start_of(low), Some(low))
}

/// Reads `..` or `..=` and what follows it: an end, then `:step` to step by
/// more than one. Only right before the `]` may a range leave out its end.
fn range_from<L>(p: &mut Parser<L>, start: u32, low: Option<Id<Expr<L>>>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<WrittenRange<L>>>,
{
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
    p.add(
        start,
        Expr::Feature(
            WrittenRange {
                inclusive,
                start: low,
                end: high,
                step,
            }
            .into(),
        ),
    )
}
