//! Character literals: `'x'` and `b'x'`. Read, and refused by the checker
//! until a character has a type.

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

/// `'x'`, or `b'x'` when `byte`
#[derive(Clone, Copy, Debug)]
pub struct CharLiteral {
    pub byte: bool,
}

impl CharLiteral {
    /// Adds the rules that read character literals to `rules`.
    pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
        rules.prefix(TokenKind::Char, char_literal);
        rules.prefix(TokenKind::ByteChar, char_literal);
    }
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

impl ExprNode for CharLiteral {
    fn precedence(&self) -> Precedence {
        Precedence::Call
    }

    /// A character literal at `span`, which no check exists for yet.
    fn check(
        &self,
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        span: Span,
        _expected: Option<TypeId>,
    ) -> thir::Expr {
        builder.checker.not_supported(span, "character literals");
        thir::Expr::INVALID
    }
}

#[cfg(test)]
mod tests;
