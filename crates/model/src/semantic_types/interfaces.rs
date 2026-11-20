//! Interfaces: the type an interface declaration makes, and the
//! implementations of it that interface references carry.

use crate::DefId;

use super::ImplementationId;
use super::Type;
use super::TypeId;
use super::Types;

impl ImplementationId {
    /// Its place in `Types`'s list, which an interface reference's second
    /// word holds.
    pub fn index(self) -> usize {
        self.0 as usize
    }

    /// The one at place `index`, read back from an interface reference.
    pub fn from_index(index: usize) -> ImplementationId {
        ImplementationId(
            u32::try_from(index).expect("a program has fewer than 2^32 implementations"),
        )
    }
}

impl Types {
    /// Makes the declared name `named` the interface whose methods have the
    /// function types `methods`, under its id, so an `&Shape` written before
    /// the interface was checked, as its own methods' `&self` is, is the
    /// `&Shape` of after.
    pub fn define_interface(&mut self, named: TypeId, methods: Box<[TypeId]>) {
        let slot = &mut self.types[named.0 as usize].ty;
        let Type::Named { definition, .. } = *slot else {
            unreachable!("only a declared name becomes an interface");
        };
        *slot = Type::Interface {
            definition,
            methods,
        };
    }

    /// Adds the implementation whose functions are `methods`, in its
    /// interface's order.
    pub fn add_implementation(&mut self, methods: Box<[DefId]>) -> ImplementationId {
        self.implementations.push(methods);
        ImplementationId::from_index(self.implementations.len() - 1)
    }

    /// The functions of the implementation `id`, in its interface's order.
    pub fn implementation(&self, id: ImplementationId) -> &[DefId] {
        &self.implementations[id.index()]
    }
}
