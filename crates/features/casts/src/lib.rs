//! Casts: `value as T`. The checker gives the core's conversion of the
//! value (`Types::conversion`), so the phases after it know nothing of this
//! crate.

use arena::Id;
use syntax::ast;

pub mod check;
pub mod parse;

/// `value as ty`
#[derive(Clone, Copy, Debug)]
pub struct Cast<L: ast::Language> {
    pub value: Id<ast::Expr<L>>,
    pub ty: Id<ast::Type<L>>,
}

#[cfg(test)]
mod tests;
