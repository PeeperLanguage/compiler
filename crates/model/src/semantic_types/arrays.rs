//! `[N]T`: arrays, and the arrays in arrays they can be.

use super::ArrayInternals;
use super::Type;
use super::TypeId;
use super::Types;

impl Types {
    /// The type `[length]element`.
    pub fn array(&mut self, element: TypeId, length: u64) -> TypeId {
        let (base, inner) = match self[element] {
            Type::Array { internals, .. } => (internals.base, internals.count),
            _ => (self.real(element), 1),
        };
        let count = inner.saturating_mul(length);
        self.intern(Type::Array {
            element,
            length,
            internals: ArrayInternals { base, count },
        })
    }

    /// The lengths of the arrays in arrays that `id` is, outermost first;
    /// none for any other type.
    pub fn array_lengths(&self, mut id: TypeId) -> impl Iterator<Item = u64> {
        std::iter::from_fn(move || match self[id] {
            Type::Array {
                element, length, ..
            } => {
                id = element;
                Some(length)
            }
            _ => None,
        })
    }
}
