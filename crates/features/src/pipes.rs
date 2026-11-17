//! Pipes: `value |> f(arguments)` is the call `f(value, arguments)`.

use arena::Id;
use check::builder::ThirBuilder;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::ExprNode;
use crate::FeatureExpr;
use crate::Peeper;

/// `value |> target`, as written
#[derive(Clone, Copy, Debug)]
pub struct Pipe {
    pub value: Id<ast::Expr<Peeper>>,
    pub target: Id<ast::Expr<Peeper>>,
}

impl Pipe {
    /// Adds the rule that reads pipes to `rules`.
    pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
        rules.infix(TokenKind::PipeArrow, Precedence::Pipe, pipe);
    }
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

impl ExprNode for Pipe {
    // A pipe reads its target at prefix precedence.
    fn precedence(&self) -> Precedence {
        Precedence::Prefix
    }

    /// `value |> target` at `span`: its target must be a call, which is checked
    /// with the value as its first written argument, so a method takes it after
    /// its `self`.
    fn check(
        &self,
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        span: Span,
        _expected: Option<TypeId>,
    ) -> thir::Expr {
        let tree = &builder.checker.module().ast;
        let ast::Expr::Call {
            callee,
            type_arguments,
            arguments,
        } = tree.exprs[self.target]
        else {
            let target = tree.exprs.span(self.target);
            let message = "a pipe's target must be a call";
            builder
                .checker
                .error(diagnostics::INVALID_OPERATION, message, target);
            builder.add_expr(self.value, None); // for its own errors
            return thir::Expr::INVALID;
        };
        let mut given = vec![self.value];
        given.extend(tree.children.lists.items(arguments));
        builder.check_call(callee, type_arguments, &given, span)
    }
}

#[cfg(test)]
mod tests;
