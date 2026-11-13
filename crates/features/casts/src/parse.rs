//! Reading `value as T`.

use arena::Id;
use parser::Grammar;
use parser::Parser;
use parser::Rules;
use syntax::ast::Expr;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::Cast;

/// Adds the rule that reads casts to `rules`.
pub const fn rows<L>(rules: &mut Rules<L>)
where
    L: Grammar<Expr: From<Cast<L>>>,
{
    rules.infix(TokenKind::As, Precedence::Cast, cast);
}

/// Reads `as Type` after `value`.
fn cast<L>(p: &mut Parser<L>, value: Id<Expr<L>>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<Cast<L>>>,
{
    p.next();
    let ty = p.type_expr();
    p.add(p.start_of(value), Expr::Feature(Cast { value, ty }.into()))
}
