//! Reading `true` and `false`.

use arena::Id;
use parser::Grammar;
use parser::Parser;
use parser::Rules;
use syntax::ast::Expr;
use token::TokenKind;

use crate::BoolLiteral;

/// Adds the rules that read bools to `rules`.
pub const fn rows<L>(rules: &mut Rules<L>)
where
    L: Grammar<Expr: From<BoolLiteral>>,
{
    rules.prefix(TokenKind::True, bool_literal);
    rules.prefix(TokenKind::False, bool_literal);
}

/// Reads `true` or `false`.
fn bool_literal<L>(p: &mut Parser<L>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<BoolLiteral>>,
{
    let token = p.next();
    let value = token.kind == TokenKind::True;
    p.add(
        token.span.start,
        Expr::Feature(BoolLiteral { value }.into()),
    )
}
