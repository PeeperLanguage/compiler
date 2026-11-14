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

use crate::FeatureExpr;
use crate::Peeper;

/// `'x'`, or `b'x'` when `byte`
#[derive(Clone, Copy, Debug)]
pub struct CharLiteral {
    pub byte: bool,
}

/// Adds the rules that read character literals to `rules`.
pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
    rules.prefix(TokenKind::Char, char_literal);
    rules.prefix(TokenKind::ByteChar, char_literal);
}

/// Reads `'x'` or `b'x'`.
fn char_literal(p: &mut Parser<Peeper>) -> Id<ast::Expr<Peeper>> {
    let token = p.next();
    let byte = token.kind == TokenKind::ByteChar;
    p.add(
        token.span.start,
        ast::Expr::Feature(FeatureExpr::Char(CharLiteral { byte })),
    )
}

/// A character literal at `span`, which no check exists for yet.
pub(crate) fn check_literal(builder: &mut ThirBuilder<'_, '_, Peeper>, span: Span) -> thir::Expr {
    builder.checker.not_supported(span, "character literals");
    thir::Expr::INVALID
}

#[cfg(test)]
mod tests;
