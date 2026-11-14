//! Pipes: `value |> f(arguments)` is the call `f(value, arguments)`.

use arena::Id;
use check::builder::ThirBuilder;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::FeatureExpr;
use crate::Peeper;

/// `value |> target`, as written
#[derive(Clone, Copy, Debug)]
pub struct Pipe {
    pub value: Id<ast::Expr<Peeper>>,
    pub target: Id<ast::Expr<Peeper>>,
}

/// Adds the rule that reads pipes to `rules`.
pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
    rules.infix(TokenKind::PipeArrow, Precedence::Pipe, pipe);
}

/// Reads `|> target` after `value`. The target takes only what binds
/// tighter than a prefix operator, so `a |> len() as i32` is
/// (a |> len()) as i32.
fn pipe(p: &mut Parser<Peeper>, value: Id<ast::Expr<Peeper>>) -> Id<ast::Expr<Peeper>> {
    p.next();
    let target = p.expr(Precedence::Prefix);
    let pipe = FeatureExpr::Pipe(Pipe { value, target });
    p.add(p.start_of(value), ast::Expr::Feature(pipe))
}

/// `value |> target` at `span`: its target must be a call, which is checked
/// with the value as its first written argument, so a method takes it after
/// its `self`.
pub(crate) fn check_pipe(
    builder: &mut ThirBuilder<'_, '_, Peeper>,
    pipe: Pipe,
    span: Span,
) -> thir::Expr {
    let tree = &builder.checker.module().ast;
    let ast::Expr::Call { callee, arguments } = tree.exprs[pipe.target] else {
        let target = tree.exprs.span(pipe.target);
        let message = "a pipe's target must be a call";
        builder
            .checker
            .error(diagnostics::INVALID_OPERATION, message, target);
        builder.add_expr(pipe.value, None); // for its own errors
        return thir::Expr::INVALID;
    };
    let mut given = vec![pipe.value];
    given.extend(tree.children.lists.items(arguments));
    builder.check_call(callee, &given, span)
}

#[cfg(test)]
mod tests;
