//! Optional values: the type `?T` and `value orelse fallback`. Both are
//! read, and refused by the checker until optional values exist.

use arena::Id;
use check::Checker;
use check::builder::ThirBuilder;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

/// Adds the rules that read optional types and `orelse` to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.type_start(TokenKind::Question, optional_type);
    rules.type_start(TokenKind::QuestionQuestion, doubled_optional_type);
    rules.infix(TokenKind::Orelse, Precedence::OrElse, or_else);
}

/// Reads the type after a `?` that starts at `start`.
fn optional_type(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let inner = p.type_expr();
    let optional = ast::FeatureType::Optional(ast::OptionalType { inner });
    p.add(start, ast::Type::Feature(optional))
}

/// Reads the type after a `??`, one token holding two markers. The second
/// changes nothing (`??T` is `?T`); it is kept in the tree as written and
/// left to a lint to report.
fn doubled_optional_type(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let inner = optional_type(p, start + 1);
    let optional = ast::FeatureType::Optional(ast::OptionalType { inner });
    p.add(start, ast::Type::Feature(optional))
}

/// Reads `orelse fallback` or `orelse { block }` after `value`. The fallback
/// may itself end in `orelse`: a orelse b orelse c is a orelse (b orelse c).
fn or_else(p: &mut Parser, value: Id<ast::Expr>) -> Id<ast::Expr> {
    p.next();
    let fallback = if p.at(TokenKind::LeftBrace) {
        ast::OrElseFallback::Block(p.block())
    } else {
        // One below OrElse: groups to the right.
        ast::OrElseFallback::Value(p.expr(Precedence::Lowest))
    };
    let or_else = ast::FeatureExpr::OrElse(ast::OrElse { value, fallback });
    p.add(p.start_of(value), ast::Expr::Feature(or_else))
}

#[cfg(test)]
mod tests;

/// `?T` at `span`, which no check exists for yet.
pub(crate) fn resolve_optional_type(
    checker: &mut Checker<'_>,
    _ty: ast::OptionalType,
    span: Span,
) -> TypeId {
    checker.not_supported(span, "optional types")
}

/// `value orelse fallback` at `span`, which no check exists for yet.
pub(crate) fn check_or_else(
    builder: &mut ThirBuilder<'_, '_>,
    _or_else: ast::OrElse,
    span: Span,
) -> thir::Expr {
    builder.checker.not_supported(span, "`orelse` fallbacks");
    thir::Expr::INVALID
}
