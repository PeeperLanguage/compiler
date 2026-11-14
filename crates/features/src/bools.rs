//! Bools: `true` and `false`. The checker gives the core's bool constant,
//! so the phases after it know nothing of them. The `bool` type and
//! its operations (`!`, `&&`, `==`) are the core's: they read the checked
//! type.

use arena::Id;
use model::constant::Value;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use syntax::ast;
use token::TokenKind;

use crate::FeatureExpr;
use crate::Peeper;

/// `true` or `false`
#[derive(Clone, Copy, Debug)]
pub struct BoolLiteral {
    pub value: bool,
}

/// Adds the rules that read bools to `rules`.
pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
    rules.prefix(TokenKind::True, bool_literal);
    rules.prefix(TokenKind::False, bool_literal);
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

/// The constant `literal` is.
pub(crate) fn check_literal(literal: BoolLiteral) -> thir::Expr {
    thir::Expr {
        ty: TypeId::BOOL,
        kind: thir::ExprKind::Constant(Value::Bool(literal.value)),
    }
}

#[cfg(test)]
mod tests;
