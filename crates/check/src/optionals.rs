//! Optional types and `orelse` in the checker. Both are refused until
//! optional values exist.

use crate::Checker;
use crate::builder;
use model::semantic_types;
use model::thir;
use source::Span;

/// `?T` at `span`, which no check exists for yet.
pub(crate) fn resolve_optional_type(
    checker: &mut Checker<'_>,
    span: Span,
) -> semantic_types::TypeId {
    checker.not_supported(span, "optional types")
}

/// `value orelse fallback` at `span`, which no check exists for yet.
pub(crate) fn check_or_else(builder: &mut builder::ThirBuilder<'_, '_>, span: Span) -> thir::Expr {
    builder.checker.not_supported(span, "`orelse` fallbacks");
    thir::Expr::INVALID
}

#[cfg(test)]
mod tests {
    //! Refusing optional types and `orelse` in the checker.

    use crate::testing;

    /// Neither is checked yet, so each is refused where it is written.
    #[test]
    fn refused() {
        let (_, reported) = testing::bodies("fn f(a: ?i32) {}\nfn g(b: i32) { b orelse 1; }");
        let found: Vec<(&str, &str)> = (reported.iter())
            .map(|(_, message, text)| (message.as_str(), text.as_str()))
            .collect();
        assert_eq!(
            found,
            [
                ("optional types are not supported yet", "?i32"),
                ("`orelse` fallbacks are not supported yet", "b orelse 1"),
            ]
        );
    }
}
