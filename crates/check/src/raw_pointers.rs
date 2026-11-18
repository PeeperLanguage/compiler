//! Raw pointers in the checker. The checker gives a `Type::RawPointer` and an
//! `ExprKind::Address`, so the phases after it know nothing of this file. A
//! pointer's operations (`==`, `rawptr + usize`), `.*` through it and `none`
//! read that checked type wherever they are checked.

use crate::builder;
use arena::Id;
use model::semantic_types;
use model::thir;
use source::Span;
use tree::ast;

use crate::Checker;

/// The type `rawptr`, or `*target`, written at `span`.
pub(crate) fn resolve_pointer_type(
    checker: &mut Checker<'_>,
    target: Option<Id<ast::Type>>,
    span: Span,
) -> semantic_types::TypeId {
    let Some(written) = target else {
        return semantic_types::TypeId::RAWPTR;
    };
    // A pointer only names its target, so a struct can point to itself.
    // shortcut: `*[2]Node` or `*Alias` of `Node` in `Node` is a cycle, as
    // an array's element and an alias's target are complete, and so is
    // any cycle through such a pointer; let them stay names too if a
    // program needs it.
    let pointee = checker.resolve_type(written);
    match checker.written_pointer_target(pointee, span) {
        Some(target) => checker.types.intern(semantic_types::Type::RawPointer {
            target: Some(target),
        }),
        None => semantic_types::TypeId::INVALID,
    }
}

/// `@place`, written at `span`.
pub(crate) fn check_raw_address(
    builder: &mut builder::ThirBuilder<'_, '_>,
    place: Id<ast::Expr>,
    span: Span,
) -> thir::Expr {
    let checked_place = builder.add_expr(place, None);
    builder.check_address(checked_place, span)
}
