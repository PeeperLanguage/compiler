//! Pipes: `value |> f(arguments)` is the call `f(value, arguments)`.

use std::num::NonZeroU32;

use arena::Id;
use diagnostics::Diagnostic;
use parser::Parser;
use parser::Rules;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::Peeper;

/// Adds the rule that reads pipes to `rules`.
pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
    rules.infix(TokenKind::PipeArrow, Precedence::Pipe, pipe);
}

/// Reads `|> f(arguments)` after `value`, as the call `f(value, arguments)`:
/// the tree holds what a pipe means, so nothing after the parser knows of
/// pipes. Its right side takes only what binds tighter than a prefix
/// operator, a call, so `a |> len() as i32` is (a |> len()) as i32.
fn pipe(p: &mut Parser<Peeper>, value: Id<ast::Expr<Peeper>>) -> Id<ast::Expr<Peeper>> {
    p.next();
    let target = p.expr(Precedence::Prefix);
    let ast::Expr::Call { callee, arguments } = p.tree.exprs[target] else {
        let span = p.tree.exprs.span(target);
        p.repair(
            Diagnostic::new(
                diagnostics::INVALID_EXPRESSION,
                "expected a call after `|>`",
            )
            .label(&p.tree.file, span, "write `value |> function(...)`"),
        );
        return value;
    };
    let mut given: Vec<NonZeroU32> = vec![value.into()];
    given.extend((p.tree.children.lists.items(arguments)).map(NonZeroU32::from));
    let arguments = p.tree.children.lists.add(&given);
    p.add(p.start_of(value), ast::Expr::Call { callee, arguments })
}

#[cfg(test)]
mod tests;
