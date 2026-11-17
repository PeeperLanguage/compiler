//! Character literals: `'x'` and `b'x'`. Read, and refused by the checker
//! until a character has a type.

use arena::Id;
use check::builder::ThirBuilder;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use token::TokenKind;

/// Adds the rules that read character literals to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.prefix(TokenKind::Char, char_literal);
    rules.prefix(TokenKind::ByteChar, char_literal);
}

/// Reads `'x'` or `b'x'`.
fn char_literal(p: &mut Parser) -> Id<ast::Expr> {
    let token = p.next();
    let byte = token.kind == TokenKind::ByteChar;
    p.add(
        token.span.start,
        ast::Expr::Feature(ast::FeatureExpr::Char(ast::CharLiteral { byte })),
    )
}

#[cfg(test)]
mod tests;

/// A character literal at `span`, which no check exists for yet.
pub(crate) fn check_char_literal(
    builder: &mut ThirBuilder<'_, '_>,
    _literal: ast::CharLiteral,
    span: Span,
) -> thir::Expr {
    builder.checker.not_supported(span, "character literals");
    thir::Expr::INVALID
}
