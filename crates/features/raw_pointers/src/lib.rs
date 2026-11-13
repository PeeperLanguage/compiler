//! Raw pointers: `rawptr`, `*T` and `@x`. The checker gives the core's raw
//! pointer type and address (`Type::RawPointer`, `ExprKind::Address`), so
//! the phases after it know nothing of this crate. A pointer's operations
//! (`==`, `rawptr + usize`), `.*` through it and `none` are the core's: they
//! read the checked type.

use arena::Id;
use syntax::ast;

pub mod check;
pub mod parse;

/// `*T`, or `rawptr` without a `target`
#[derive(Clone, Copy, Debug)]
pub struct PointerType<L: ast::Language> {
    pub target: Option<Id<ast::Type<L>>>,
}

/// `@place`
#[derive(Clone, Copy, Debug)]
pub struct RawAddress<L: ast::Language> {
    pub place: Id<ast::Expr<L>>,
}

#[cfg(test)]
mod tests;
