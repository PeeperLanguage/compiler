//! `range[T]`: its type and the fields of its values.

use super::Integer;
use super::Type;
use super::TypeId;
use super::Types;

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
    /// is looked through), or an open one (`is_open`), which each copy
    /// checks. Its step's type is the unsigned integer as wide as `element`.
    /// `None` when `element` is neither.
    pub fn range(&mut self, element: TypeId) -> Option<TypeId> {
        let element = self.real(element);
        let Type::Integer(integer) = self[element] else {
            return (self.is_open(element)).then(|| self.intern(Type::Range { element }));
        };
        // Its step's type, which `range_step` finds.
        self.intern(Type::Integer(Integer {
            signed: false,
            ..integer
        }));
        Some(self.intern(Type::Range { element }))
    }

    /// The type of a step of a `range[element]` of an integer `element`:
    /// the unsigned integer as wide as it. Only `range` makes a range type,
    /// and it makes this type first, so it is there to find.
    pub fn range_step(&self, element: TypeId) -> TypeId {
        let Type::Integer(integer) = self[element] else {
            unreachable!("a range is of integers")
        };
        self.ids[&Type::Integer(Integer {
            signed: false,
            ..integer
        })]
    }

    /// The fields of a value of `range[element]`, in order, each with its
    /// name, as debug info shows it, and type: its walk, found once when the
    /// range is made (`lower::Lowering::walk_range`). Its values are
    /// `start`, then each `delta` more than the one before, wrapping, while
    /// `left`, how far its end is, lasts by `step`, the end counted when
    /// `inclusive`. `field` gives where each one is.
    pub fn range_fields(&self, element: TypeId) -> [(&'static str, TypeId); 5] {
        let step = self.range_step(element);
        [
            ("start", element),
            ("left", step),
            ("step", step),
            ("delta", element),
            ("inclusive", TypeId::BOOL),
        ]
    }
}
