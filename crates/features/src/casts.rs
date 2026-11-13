//! Casts: `value as T`, read and checked into the core's conversion of the
//! value (`Types::conversion`), so the phases after checking know nothing
//! of them.

use arena::Id;
use check::builder::ThirBuilder;
use model::semantic_types::Conversion;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

use crate::FeatureExpr;
use crate::Peeper;

/// `value as ty`
#[derive(Clone, Copy, Debug)]
pub struct Cast {
    pub value: Id<ast::Expr<Peeper>>,
    pub ty: Id<ast::Type<Peeper>>,
}

/// Adds the rule that reads casts to `rules`.
pub(crate) const fn rows(rules: &mut Rules<Peeper>) {
    rules.infix(TokenKind::As, Precedence::Cast, cast);
}

/// Reads `as Type` after `value`.
fn cast(p: &mut Parser<Peeper>, value: Id<ast::Expr<Peeper>>) -> Id<ast::Expr<Peeper>> {
    p.next();
    let ty = p.type_expr();
    let cast = FeatureExpr::Cast(Cast { value, ty });
    p.add(p.start_of(value), ast::Expr::Feature(cast))
}

/// `value as ty`: `value` as a value of the type `ty`, which its type
/// converts to with `as` or without it (`Types::conversion`). A number
/// literal takes the type `ty` if it can, so `300 as u8` is an error.
pub(crate) fn check_cast(
    builder: &mut ThirBuilder<'_, '_, Peeper>,
    cast: Cast,
    span: Span,
) -> thir::Expr {
    let Cast { value, ty } = cast;
    let to = builder.checker.resolve_value_type(ty);
    let checked = builder.check_expr(value, Some(to));
    if checked.ty == TypeId::INVALID || to == TypeId::INVALID {
        return thir::Expr::INVALID;
    }
    let value_span = builder.checker.module().ast.exprs.span(value);
    let found = checked.ty;
    let conversion = builder.checker.types.conversion(found, to);
    if let Some(converted) = builder.convert(checked, value_span, to, conversion) {
        return converted;
    }
    let (found_text, to_text) = builder.checker.type_texts(found, to);
    let message = format!("`{found_text}` cannot be converted to `{to_text}`");
    let types = &builder.checker.types;
    let diagnostic = builder.type_error(
        diagnostics::INVALID_CAST,
        message,
        span,
        value,
        found,
        |ty| types.conversion(ty, to) != Conversion::Never,
    );
    builder.checker.report(diagnostic);
    thir::Expr::INVALID
}

#[cfg(test)]
mod tests;
