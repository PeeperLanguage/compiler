//! Reading `"text"` and `c"text"`.

use arena::Id;
use parser::Grammar;
use parser::Parser;
use parser::Rules;
use syntax::ast::Expr;
use syntax::ast::StringKind;
use token::TokenKind;

use crate::StringLiteral;

/// Adds the rules that read string literals to `rules`.
pub const fn rows<L>(rules: &mut Rules<L>)
where
    L: Grammar<Expr: From<StringLiteral>>,
{
    rules.prefix(TokenKind::String, string_literal);
    rules.prefix(TokenKind::CString, string_literal);
}

/// Reads `"text"` or `c"text"`.
fn string_literal<L>(p: &mut Parser<L>) -> Id<Expr<L>>
where
    L: Grammar<Expr: From<StringLiteral>>,
{
    let token = p.next();
    let kind = match token.kind {
        TokenKind::CString => StringKind::C,
        _ => StringKind::Plain,
    };
    p.add(
        token.span.start,
        Expr::Feature(StringLiteral { kind }.into()),
    )
}
