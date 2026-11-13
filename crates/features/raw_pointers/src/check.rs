//! Checking raw pointer types and `@x` into the core's raw pointer type
//! (`Type::RawPointer`) and address (`ExprKind::Address`).

use check::Checked;
use check::Checker;
use check::builder::ThirBuilder;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir::Expr;
use source::Span;

use crate::PointerType;
use crate::RawAddress;

/// The type `rawptr`, or `*target`, written at `span`.
pub fn resolve_type<L: Checked>(
    checker: &mut Checker<'_, L>,
    ty: PointerType<L>,
    span: Span,
) -> TypeId {
    let Some(target) = ty.target else {
        return TypeId::RAWPTR;
    };
    // A pointer only names its target, so a struct can point to itself.
    // shortcut: `*[2]Node` or `*Alias` of `Node` in `Node` is a cycle, as
    // an array's element and an alias's target are complete, and so is
    // any cycle through such a pointer; let them stay names too if a
    // program needs it.
    let pointee = checker.resolve_type(target);
    match checker.written_pointer_target(pointee, span) {
        Some(target) => checker.types.intern(Type::RawPointer {
            target: Some(target),
        }),
        None => TypeId::INVALID,
    }
}

/// `@place`, written at `span`.
pub fn check_address<L: Checked>(
    builder: &mut ThirBuilder<'_, '_, L>,
    address: RawAddress<L>,
    span: Span,
) -> Expr {
    let place = builder.add_expr(address.place, None);
    builder.check_address(place, span)
}
