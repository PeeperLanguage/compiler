//! Checking string literals into the core's `str` and `cstr` constants.

use check::Checked;
use check::builder::ThirBuilder;
use model::semantic_types::TypeId;
use model::thir::Expr;
use source::Span;
use syntax::ast::StringKind;

use crate::StringLiteral;

/// The constant the string literal `literal`, written at `span`, is.
pub fn check_literal<L: Checked>(
    builder: &mut ThirBuilder<'_, '_, L>,
    literal: StringLiteral,
    span: Span,
) -> Expr {
    let ty = match literal.kind {
        StringKind::Plain => TypeId::STR,
        StringKind::C => TypeId::CSTR,
    };
    builder.string_literal(span, ty)
}
