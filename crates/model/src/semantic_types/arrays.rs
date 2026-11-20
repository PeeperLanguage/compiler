//! `[N]T`: arrays, and the arrays in arrays they can be.

use super::ArrayInternals;
use super::CheckedConstant;
use super::Constant;
use super::Type;
use super::TypeId;
use super::Types;
use crate::constant::Value;

impl Types {
    /// The type `[length]element`, `length` a `usize` constant
    /// (`array_length`), or one each copy gives (`CheckedConstant::PerCopy`).
    pub fn array(&mut self, element: TypeId, length: CheckedConstant) -> TypeId {
        let (base, inner) = match self[element] {
            Type::Array { internals, .. } => (internals.base, internals.count),
            _ => (self.real(element), Some(1)),
        };
        let count = inner.zip(length.known_length());
        let count = count.map(|(inner, length)| inner.saturating_mul(length));
        self.intern(Type::Array {
            element,
            length,
            internals: ArrayInternals { base, count },
        })
    }

    /// The array length `length`, a `usize` constant.
    pub fn array_length(&self, length: u64) -> CheckedConstant {
        let Type::Integer(usize) = self[self.usize] else {
            unreachable!("`usize` is an integer type")
        };
        CheckedConstant::Known(Constant {
            ty: self.usize,
            value: Value::Integer(bigint::Limbs::new(usize.bits, &[length])),
        })
    }

    /// The lengths of the arrays in arrays that `id` is, outermost first;
    /// none for any other type.
    pub fn array_lengths(&self, mut id: TypeId) -> impl Iterator<Item = &CheckedConstant> {
        std::iter::from_fn(move || match &self[id] {
            Type::Array {
                element, length, ..
            } => {
                id = *element;
                Some(length)
            }
            _ => None,
        })
    }
}

impl CheckedConstant {
    /// Its value as an array length, a `usize`, when known now.
    pub(crate) fn known_length(&self) -> Option<u64> {
        match self {
            CheckedConstant::Known(Constant {
                value: Value::Integer(length),
                ..
            }) => Some(length.as_slice()[0]),
            _ => None,
        }
    }

    /// Its value as the length of an array in a type that is laid out.
    pub fn laid_out_length(&self) -> u64 {
        // An open type is never laid out: no copy is made from it.
        self.known_length()
            .expect("only a type whose lengths are all known is laid out")
    }
}
