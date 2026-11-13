//! Interfaces: `iface { fn name(&self) -> Result, ... }`. The checker gives
//! the core's interface type, so the phases after it know nothing of this
//! crate. Interface references, owned interfaces and the conversion of a
//! struct to an interface are the core's: they read the checked type.

use arena::List;
use syntax::ast;

pub mod check;
pub mod parse;

/// `iface { methods }`: `Decl::Function`s without bodies.
#[derive(Clone, Copy, Debug)]
pub struct InterfaceType<L: ast::Language> {
    pub methods: List<ast::Decl<L>>,
}

#[cfg(test)]
mod tests;
