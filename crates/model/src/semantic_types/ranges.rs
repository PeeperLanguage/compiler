//! `range[T]`: its type and the fields of its values.

use super::Integer;
use super::Type;
use super::TypeId;
use super::Types;

/// The fields of a value of `Type::Range { element, step }`, in order, each
/// with its name, as debug info shows it, and type: its walk, found once
/// when the range is made (`lower::Lowering::walk_range`). Its values are
/// `start`, then each `delta` more than the one before, wrapping, while
/// `left`, how far its end is, lasts by `step`, the end counted when
/// `inclusive`. `field` gives where each one is.
pub fn range_fields(element: TypeId, step: TypeId) -> [(&'static str, TypeId); 5] {
    [
        ("start", element),
        ("left", step),
        ("step", step),
        ("delta", element),
        ("inclusive", TypeId::BOOL),
    ]
}

/// Where each field of a range value is in `range_fields`.
pub mod field {
    pub const START: usize = 0;
    pub const LEFT: usize = 1;
    pub const STEP: usize = 2;
    pub const DELTA: usize = 3;
    pub const INCLUSIVE: usize = 4;
}

impl Types {
    /// The type `range[element]`, for the integer type `element` (an alias
    /// is looked through). Its step's type is the unsigned integer as wide
    /// as `element`. `None` when `element` is not an integer.
    pub fn range(&mut self, element: TypeId) -> Option<TypeId> {
        let Type::Integer(integer) = self[element] else {
            return None;
        };
        let element = self.real(element);
        let step = self.intern(Type::Integer(Integer {
            signed: false,
            ..integer
        }));
        Some(self.intern(Type::Range { element, step }))
    }
}
