//! Character literals: `'x'` and `b'x'`. Refused by the checker until a
//! character has a type.

use crate::builder;
use model::thir;
use source::Span;

/// A character literal at `span`, which no check exists for yet.
pub(crate) fn check_char_literal(
    builder: &mut builder::ThirBuilder<'_, '_>,
    span: Span,
) -> thir::Expr {
    builder.checker.not_supported(span, "character literals");
    thir::Expr::INVALID
}

#[cfg(test)]
mod tests {
    //! Refusal of character literals until they have a type.

    use crate::testing;

    /// A character has no type yet, so a literal is refused where it is
    /// written.
    #[test]
    fn refused() {
        let (_, reported) = testing::bodies("fn f() { 'x'; }");
        let found: Vec<(&str, &str)> = (reported.iter())
            .map(|(_, message, text)| (message.as_str(), text.as_str()))
            .collect();
        assert_eq!(found, [("character literals are not supported yet", "'x'")]);
    }
}
