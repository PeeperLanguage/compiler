//! Raw pointers: `rawptr`, `*T` and `@x`. The checker gives the core's raw
//! pointer type and address (`Type::RawPointer`, `ExprKind::Address`), so
//! the phases after it know nothing of them. A pointer's operations
//! (`==`, `rawptr + usize`), `.*` through it and `none` are the core's: they
//! read the checked type.

use arena::Id;
use check::Checker;
use check::builder::ThirBuilder;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::thir;
use parser::Parser;
use parser::Rules;
use source::Span;
use syntax::ast;
use syntax::operators::Precedence;
use token::TokenKind;

/// Adds the rules that read raw pointer types and `@place` to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.type_start(TokenKind::Asterisk, typed_pointer);
    rules.type_start(TokenKind::Rawptr, raw_pointer);
    // types that cannot be values
    rules.prefix(TokenKind::Asterisk, type_value);
    rules.prefix(TokenKind::Rawptr, type_value);
    rules.prefix(TokenKind::At, raw_address);
}

/// Reads the `T` of `*T`, a pointer type that starts at `start`.
fn typed_pointer(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let target = Some(p.type_expr());
    p.add(
        start,
        ast::Type::Feature(ast::FeatureType::Pointer(ast::PointerType { target })),
    )
}

/// `rawptr`, which starts at `start`.
fn raw_pointer(p: &mut Parser, start: u32) -> Id<ast::Type> {
    p.add(
        start,
        ast::Type::Feature(ast::FeatureType::Pointer(ast::PointerType { target: None })),
    )
}

/// Reads a type that cannot be a value (`*T`, `rawptr`) where a value could
/// go.
fn type_value(p: &mut Parser) -> Id<ast::Expr> {
    let start = p.tok().span.start;
    let ty = p.type_expr();
    p.add(start, ast::Expr::Type(ty))
}

/// Reads `@x`.
fn raw_address(p: &mut Parser) -> Id<ast::Expr> {
    let start = p.next().span.start;
    let place = p.expr(Precedence::Prefix);
    p.add(
        start,
        ast::Expr::Feature(ast::FeatureExpr::RawAddress(ast::RawAddress { place })),
    )
}

#[cfg(test)]
mod tests;

/// The type `rawptr`, or `*target`, written at `span`.
pub(crate) fn resolve_pointer_type(
    checker: &mut Checker<'_>,
    ty: ast::PointerType,
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
pub(crate) fn check_raw_address(
    builder: &mut ThirBuilder<'_, '_>,
    address: ast::RawAddress,
    span: Span,
) -> thir::Expr {
    let place = builder.add_expr(address.place, None);
    builder.check_address(place, span)
}
