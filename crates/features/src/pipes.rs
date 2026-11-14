//! Pipes: `value |> f(arguments)`. Read, and refused by the checker until
//! a pipe is checked as its call.

use arena::Id;
use check::builder::ThirBuilder;
use diagnostics::Diagnostic;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::FeatureExpr;
use crate::Peeper;

/// `value |> call`, kept as written; the call is a core call
#[derive(Clone, Copy, Debug)]
pub struct Pipe {
    pub value: Id<ast::Expr<Peeper>>,
    pub call: Id<ast::Expr<Peeper>>,
}

/// Adds the rule that reads pipes to `rules`.
pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
    rules.infix(TokenKind::PipeArrow, Precedence::Pipe, pipe);
}

/// Reads `|> f(arguments)` after `value`. Its right side takes only what
/// binds tighter than a prefix operator, a call, so `a |> len() as i32` is
/// (a |> len()) as i32. The target must call a function, not a method.
fn pipe(p: &mut Parser<Peeper>, value: Id<ast::Expr<Peeper>>) -> Id<ast::Expr<Peeper>> {
    p.next();
    let call = p.expr(Precedence::Prefix);
    match p.tree.exprs[call] {
        ast::Expr::Call { callee, .. }
            if matches!(p.tree.exprs[callee], ast::Expr::Selector { .. }) =>
        {
            let span = p.tree.exprs.span(callee);
            p.repair(
                Diagnostic::new(diagnostics::INVALID_EXPRESSION, "pipe cannot call a method")
                    .label(&p.tree.file, span, "use `value.method(...)`"),
            );
        }
        ast::Expr::Call { .. } => {}
        _ => {
            let span = p.tree.exprs.span(call);
            p.repair(
                Diagnostic::new(
                    diagnostics::INVALID_EXPRESSION,
                    "pipe target must be a free-function call",
                )
                .label(&p.tree.file, span, "write `value |> function(...)`"),
            );
        }
    }
    let pipe = FeatureExpr::Pipe(Pipe { value, call });
    p.add(p.start_of(value), ast::Expr::Feature(pipe))
}

/// `value |> call` at `span`, which no check exists for yet.
pub(crate) fn check_pipe(builder: &mut ThirBuilder<'_, '_, Peeper>, span: Span) -> thir::Expr {
    builder.checker.not_supported(span, "pipes");
    thir::Expr::INVALID
}

#[cfg(test)]
mod tests;
