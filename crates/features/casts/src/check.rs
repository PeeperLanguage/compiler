//! Checking `value as T` into the core's conversion of the value.

use check::Checked;
use check::builder::ThirBuilder;
use model::semantic_types::Conversion;
use model::semantic_types::TypeId;
use model::thir::Expr;
use source::Span;

use crate::Cast;

/// `value as ty`: `value` as a value of the type `ty`, which its type
/// converts to with `as` or without it (`Types::conversion`). A number
/// literal takes the type `ty` if it can, so `300 as u8` is an error.
pub fn check_cast<L: Checked>(
    builder: &mut ThirBuilder<'_, '_, L>,
    cast: Cast<L>,
    span: Span,
) -> Expr {
    let Cast { value, ty } = cast;
    let to = builder.checker.resolve_value_type(ty);
    let checked = builder.check_expr(value, Some(to));
    if checked.ty == TypeId::INVALID || to == TypeId::INVALID {
        return Expr::INVALID;
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
    Expr::INVALID
}
