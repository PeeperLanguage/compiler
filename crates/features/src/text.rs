//! Text: string literals, `"..."` and `c"..."`. The checker gives the
//! core's `str` or `cstr` constant, so the phases after it know nothing of
//! them. The `str` and `cstr` types are the core's; the core reads a
//! literal's kind through `Language::string_kind`, which the list answers (an
//! `extern`'s name, a `panic` message).

use arena::Id;
use check::builder::ThirBuilder;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::ast::StringKind;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::ExprNode;
use crate::FeatureExpr;
use crate::Peeper;

/// `"..."`, or `c"..."`; `literal::value` decodes its text
#[derive(Clone, Copy, Debug)]
pub struct StringLiteral {
    pub kind: StringKind,
}

impl StringLiteral {
    /// Adds the rules that read string literals to `rules`.
    pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
        rules.prefix(TokenKind::String, string_literal);
        rules.prefix(TokenKind::CString, string_literal);
    }
}

/// Reads `"text"` or `c"text"`.
fn string_literal(p: &mut Parser<Peeper>) -> Id<ast::Expr<Peeper>> {
    let token = p.next();
    let kind = match token.kind {
        TokenKind::CString => StringKind::C,
        _ => StringKind::Plain,
    };
    p.add(
        token.span.start,
        ast::Expr::Feature(FeatureExpr::String(StringLiteral { kind })),
    )
}

impl ExprNode for StringLiteral {
    fn precedence(&self) -> Precedence {
        Precedence::Call
    }

    fn string_kind(&self) -> Option<StringKind> {
        Some(self.kind)
    }

    /// The constant the string literal `literal`, written at `span`, is.
    fn check(
        &self,
        builder: &mut ThirBuilder<'_, '_, Peeper>,
        span: Span,
        _expected: Option<TypeId>,
    ) -> thir::Expr {
        let ty = match self.kind {
            StringKind::Plain => TypeId::STR,
            StringKind::C => TypeId::CSTR,
        };
        builder.string_literal(span, ty)
    }
}

#[cfg(test)]
mod tests;
