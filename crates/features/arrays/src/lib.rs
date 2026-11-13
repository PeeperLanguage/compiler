//! Arrays: `[N]T` and the array literals `[N]T{ ... }` and `[_]T{ ... }`.
//! The checker gives the core's array type and literal (`Type::Array`,
//! `ExprKind::ArrayLiteral`), so the phases after it know nothing of this
//! crate. A literal's entries and their `...` spreads are the core's, as a
//! call to a `...` parameter checks them too; so are slices, read after the
//! core's `&`, and indexing.

use arena::Id;
use arena::List;
use source::Span;
use syntax::ast;

pub mod check;
pub mod parse;

/// `[length]element`
#[derive(Clone, Copy, Debug)]
pub struct ArrayType<L: ast::Language> {
    /// the written number
    pub length: Span,
    pub element: Id<ast::Type<L>>,
}

/// `[N]T{ entries }` or `[_]T{ entries }`; each entry is a value or a
/// `values...` spread.
#[derive(Clone, Copy, Debug)]
pub struct ArrayLiteral<L: ast::Language> {
    pub ty: LiteralType<L>,
    pub entries: List<ast::Expr<L>>,
}

/// The type an array literal is written with.
#[derive(Clone, Copy, Debug)]
pub enum LiteralType<L: ast::Language> {
    /// `[N]T`
    Written(Id<ast::Type<L>>),
    /// `[_]T`: its length is the number of values its entries give
    Counted { element: Id<ast::Type<L>> },
}

#[cfg(test)]
mod tests;
