//! Bools: `true` and `false`. The checker gives the core's bool constant,
//! so the phases after it know nothing of them. The `bool` type and
//! its operations (`!`, `&&`, `==`) are the core's: they read the checked
//! type.

use arena::Id;
use check::builder::ThirBuilder;
use model::constant::Value;
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

/// `true` or `false`
#[derive(Clone, Copy, Debug)]
pub struct BoolLiteral {
    pub value: bool,
}

impl BoolLiteral {
    /// Adds the rules that read bools to `rules`.
    pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
        rules.prefix(TokenKind::True, bool_literal);
        rules.prefix(TokenKind::False, bool_literal);
    }
}

/// Reads `true` or `false`.
fn bool_literal(p: &mut Parser<Peeper>) -> Id<ast::Expr<Peeper>> {
    let token = p.next();
    let value = token.kind == TokenKind::True;
    p.add(
        token.span.start,
        ast::Expr::Feature(FeatureExpr::Bool(BoolLiteral { value })),
    )
}

impl ExprNode for BoolLiteral {
    fn precedence(&self) -> Precedence {
        Precedence::Call
    }

    /// The constant `literal` is.
    fn check(
        &self,
        _builder: &mut ThirBuilder<'_, '_, Peeper>,
        _span: Span,
        _expected: Option<TypeId>,
    ) -> thir::Expr {
        thir::Expr {
            ty: TypeId::BOOL,
            kind: thir::ExprKind::Constant(Value::Bool(self.value)),
        }
    }
}

#[cfg(test)]
mod tests;
