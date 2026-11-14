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

use crate::FeatureExpr;
use crate::FeatureType;
use crate::Peeper;

/// `?T`
#[derive(Clone, Copy, Debug)]
pub struct OptionalType {
    pub inner: Id<ast::Type<Peeper>>,
}

/// `value orelse fallback`
#[derive(Clone, Copy, Debug)]
pub struct OrElse {
    pub value: Id<ast::Expr<Peeper>>,
    pub fallback: Fallback,
}

/// What `orelse` gives when its value is missing.
#[derive(Clone, Copy, Debug)]
pub enum Fallback {
    Value(Id<ast::Expr<Peeper>>),
    Block(Id<ast::Block<Peeper>>),
}

/// Adds the rules that read optional types and `orelse` to `rules`.
pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
    rules.type_start(TokenKind::Question, optional_type);
    rules.type_start(TokenKind::QuestionQuestion, doubled_optional_type);
    rules.infix(TokenKind::Orelse, Precedence::OrElse, or_else);
}

/// Reads the type after a `?` that starts at `start`.
fn optional_type(p: &mut Parser<Peeper>, start: u32) -> Id<ast::Type<Peeper>> {
    let inner = p.type_expr();
    let optional = FeatureType::Optional(OptionalType { inner });
    p.add(start, ast::Type::Feature(optional))
}

/// Reads the type after a `??`, one token holding two markers. The second
/// changes nothing (`??T` is `?T`); it is kept in the tree as written and
/// left to a lint to report.
fn doubled_optional_type(p: &mut Parser<Peeper>, start: u32) -> Id<ast::Type<Peeper>> {
    let inner = optional_type(p, start + 1);
    let optional = FeatureType::Optional(OptionalType { inner });
    p.add(start, ast::Type::Feature(optional))
}

/// Reads `orelse fallback` or `orelse { block }` after `value`. The fallback
/// may itself end in `orelse`: a orelse b orelse c is a orelse (b orelse c).
fn or_else(p: &mut Parser<Peeper>, value: Id<ast::Expr<Peeper>>) -> Id<ast::Expr<Peeper>> {
    p.next();
    let fallback = if p.at(TokenKind::LeftBrace) {
        Fallback::Block(p.block())
    } else {
        // One below OrElse: groups to the right.
        Fallback::Value(p.expr(Precedence::Lowest))
    };
    let or_else = FeatureExpr::OrElse(OrElse { value, fallback });
    p.add(p.start_of(value), ast::Expr::Feature(or_else))
}

/// `?T` at `span`, which no check exists for yet.
pub(crate) fn resolve_type(checker: &mut Checker<'_, Peeper>, span: Span) -> TypeId {
    checker.not_supported(span, "optional types")
}

/// `value orelse fallback` at `span`, which no check exists for yet.
pub(crate) fn check_or_else(builder: &mut ThirBuilder<'_, '_, Peeper>, span: Span) -> thir::Expr {
    builder.checker.not_supported(span, "`orelse` fallbacks");
    thir::Expr::INVALID
}

#[cfg(test)]
mod tests;
