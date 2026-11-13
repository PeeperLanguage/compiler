//! Structs: `struct { name: Type, ... }` and `Type.{ name = value, ... }`.
//! The checker gives the core's struct type and struct value, so the phases
//! after it know nothing of this crate. A field and a field's value are the
//! core's (`ast::Field`, `ast::FieldValue`): the core reads fields too, as
//! in `point.x`.

use arena::Id;
use arena::List;
use syntax::ast;

pub mod check;
pub mod parse;

/// `Type.{ name = value }`, or `.{ ... }` without a type
#[derive(Clone, Copy, Debug)]
pub struct StructLiteral<L: ast::Language> {
    pub ty: Option<Id<ast::Type<L>>>,
    pub fields: List<ast::FieldValue<L>>,
}

/// `struct { fields }`
#[derive(Clone, Copy, Debug)]
pub struct StructType<L: ast::Language> {
    pub fields: List<ast::Field<L>>,
}

#[cfg(test)]
mod tests;
