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
    /// pair not named here never converts.
    pub fn conversion(&self, from: TypeId, to: TypeId) -> Conversion {
        if self.same(from, to) {
            return Conversion::Same;
        }
        if self.is_open(from) || self.is_open(to) {
            return Conversion::Undecided;
        }
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
            ) if self.same(from, to) => Conversion::Reborrow,
            (
                &Type::Reference {
                    mutable: from_mutable,
                    target,
                },
                &Type::Slice { mutable, element },
            ) if from_mutable || !mutable => match self[target] {
                Type::Array { element: from, .. } if self.same(from, element) => {
                    Conversion::Reborrow
                }
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
            ) if self.same(from, to) => Conversion::Reborrow,
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
            ) if (from_mutable || !mutable) && self[target].members().is_some() => {
                Conversion::ToInterface
            }
            (from, Type::Interface { .. }) if from.members().is_some() => Conversion::ToInterface,
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
