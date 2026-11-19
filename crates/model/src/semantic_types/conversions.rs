//! How a value of one type becomes a value of another, decided in one
//! place by the pair of types: whether it converts, with `as` or without,
//! and for a number, a `bool` or a raw pointer, the cast that does it.

use crate::mir;

use super::Conversion;
use super::Float;
use super::Integer;
use super::Type;
use super::TypeId;
use super::Types;

impl Types {
    /// How a value of the type `from` becomes a value of the type `to`. A
    /// pair not named here never converts. A pair that holds a type each
    /// copy decides is `Undecided` when copies differ: one makes the two
    /// the same (`*T` to `*i32`, which another casts), or as written it
    /// never converts but in some copy does (`&mut T` to `&i32`, `Box<T>`
    /// to an interface). `[2]T` to `bool` converts in none.
    pub fn conversion(&self, from: TypeId, to: TypeId) -> Conversion {
        if self.same(from, to) {
            return Conversion::Same;
        }
        // The same type in some copy converts as `Same` there, whatever
        // the pair needs as written.
        if self.could_be_same(from, to) {
            return Conversion::Undecided;
        }
        // `conversion` when the parts `a` and `b` are the same; `Undecided`
        // when they are only in some copy.
        let when_same = |a: TypeId, b: TypeId, conversion: Conversion| {
            if self.same(a, b) {
                conversion
            } else if self.could_be_same(a, b) {
                Conversion::Undecided
            } else {
                Conversion::Never
            }
        };
        // A struct given an open type has no copy to hold its members, and a
        // type each copy decides may be a struct: in some copy, each has them.
        let to_interface = |ty: TypeId| {
            if self[ty].members().is_some() {
                Conversion::ToInterface
            } else if self[ty].is_decided_per_copy()
                || (self.is_open(ty) && matches!(self[ty], Type::Named { .. }))
            {
                Conversion::Undecided
            } else {
                Conversion::Never
            }
        };
        match (&self[from], &self[to]) {
            (&Type::Integer(from), &Type::Integer(to)) => {
                let cast = from.cast_to(to);
                if to.bits > from.bits && (from.signed == to.signed || to.signed) {
                    Conversion::Implicit(cast)
                } else {
                    Conversion::NeedsCast(cast)
                }
            }
            (Type::Bool, &Type::Integer(to)) => Conversion::NeedsCast(Integer::BOOL.cast_to(to)),
            (&Type::Integer(from), Type::Float(_)) => Conversion::NeedsCast(if from.signed {
                mir::Cast::SignedToFloat
            } else {
                mir::Cast::UnsignedToFloat
            }),
            (Type::Float(_), &Type::Integer(to)) => Conversion::NeedsCast(if to.signed {
                mir::Cast::FloatToSigned { bits: to.bits }
            } else {
                mir::Cast::FloatToUnsigned { bits: to.bits }
            }),
            (Type::Float(Float::F32), Type::Float(Float::F64)) => {
                Conversion::Implicit(mir::Cast::FloatWiden)
            }
            (Type::Float(Float::F64), Type::Float(Float::F32)) => {
                Conversion::NeedsCast(mir::Cast::FloatNarrow)
            }
            (
                &Type::Reference {
                    mutable: true,
                    target: from,
                },
                &Type::Reference {
                    mutable: false,
                    target: to,
                },
            ) => when_same(from, to, Conversion::Reborrow),
            (
                &Type::Reference {
                    mutable: from_mutable,
                    target,
                },
                &Type::Slice { mutable, element },
            ) if from_mutable || !mutable => match self[target] {
                Type::Array { element: from, .. } => when_same(from, element, Conversion::Reborrow),
                // In some copy, `T` is an array.
                _ if self[target].is_decided_per_copy() => Conversion::Undecided,
                _ => Conversion::Never,
            },
            (
                &Type::Slice {
                    mutable: true,
                    element: from,
                },
                &Type::Slice {
                    mutable: false,
                    element: to,
                },
            ) => when_same(from, to, Conversion::Reborrow),
            (
                &Type::InterfaceReference {
                    mutable: true,
                    interface: from,
                },
                &Type::InterfaceReference {
                    mutable: false,
                    interface: to,
                },
            ) if from == to => Conversion::Reborrow,
            (
                &Type::Reference {
                    mutable: from_mutable,
                    target,
                },
                &Type::InterfaceReference { mutable, .. },
            ) if from_mutable || !mutable => to_interface(target),
            (_, Type::Interface { .. }) => to_interface(from),
            (Type::RawPointer { target: Some(_) }, Type::RawPointer { target: None }) => {
                Conversion::Same
            }
            (Type::RawPointer { .. }, Type::RawPointer { .. }) => {
                Conversion::NeedsCast(mir::Cast::PointerCast)
            }
            _ => Conversion::Never,
        }
    }
}
