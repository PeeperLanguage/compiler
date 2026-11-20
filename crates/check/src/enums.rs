//! Enums: the checker's file for the type `enum { A, B: Payload }` and its
//! declaration, variant literals, `is` tests and `match`. Each is refused
//! until enums are checked.

use crate::builder;
use model::semantic_types;
use model::thir;
use source::Span;
use tree::ast;

use crate::Checker;

/// An enum type at `span`, which no check exists for yet.
pub(crate) fn resolve_enum_type(checker: &mut Checker<'_>, span: Span) -> semantic_types::TypeId {
    checker.not_supported(span, "enum types")
}

/// The declaration of an enum, written `written`, which no check exists
/// for yet.
pub(crate) fn declare_enum_type(
    checker: &mut Checker<'_>,
    written: ast::TypeDecl,
) -> semantic_types::TypeId {
    checker.not_supported(written.name.span, "enum declarations")
}

/// Variant literals, at `span`, which no check exists for yet.
pub(crate) fn check_variant_literal(
    builder: &mut builder::ThirBuilder<'_, '_>,
    span: Span,
) -> thir::Expr {
    builder.checker.not_supported(span, "variant literals");
    thir::Expr::INVALID
}

/// `is` tests, at `span`, which no check exists for yet.
pub(crate) fn check_case_test(
    builder: &mut builder::ThirBuilder<'_, '_>,
    span: Span,
) -> thir::Expr {
    builder.checker.not_supported(span, "`is` tests");
    thir::Expr::INVALID
}

/// A `match` at `span`, which no check exists for yet.
pub(crate) fn check_match(
    builder: &mut builder::ThirBuilder<'_, '_>,
    span: Span,
) -> Option<thir::Stmt> {
    builder.checker.not_supported(span, "`match` statements");
    None
}

#[cfg(test)]
mod tests {
    //! Refusal of enums and `match` until they are checked.

    use crate::testing;

    /// Nothing of an enum is checked yet, so each part is refused where it is
    /// written.
    #[test]
    fn refused() {
        let (_, reported) = testing::bodies(
            "enum E { A, B: i32 }
         fn f(e: E) {}
         fn g(a: i32) { E::B with a; }
         fn h(a: i32) { a is E::A; }
         fn i(a: i32) { match a { _ => { } } }",
        );
        let found: Vec<&str> = (reported.iter())
            .map(|(_, message, _)| message.as_str())
            .collect();
        assert_eq!(
            found,
            [
                "enum declarations are not supported yet",
                "variant literals are not supported yet",
                "`is` tests are not supported yet",
                "`match` statements are not supported yet",
            ]
        );
    }
}
