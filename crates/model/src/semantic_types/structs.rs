//! Structs: the type a struct declaration makes, and its fields.

use super::Members;
use super::StructInternals;
use super::Type;
use super::TypeId;
use super::Types;

impl Types {
    /// Makes the declared name `named` the struct of `fields`, laid out as
    /// `struct_layout` gives (`offsets`, `size`, `align`), under its id, so a
    /// `*T` written before `T` was complete is the `*T` of after.
    pub fn define_struct(
        &mut self,
        named: TypeId,
        fields: Box<[TypeId]>,
        (offsets, size, align): (Vec<u64>, u64, u64),
    ) {
        let internals = StructInternals {
            offsets: offsets.into(),
            size,
            align,
            holders: self.holders_in_fields(&fields),
            mutable_holder: (fields.iter()).any(|&field| self.holds_loans(field) == Some(true)),
        };
        let slot = &mut self.types[named.0 as usize].ty;
        let Type::Named {
            definition,
            arguments,
        } = *slot
        else {
            unreachable!("only a declared name becomes a struct");
        };
        *slot = Type::Struct {
            definition,
            arguments,
            fields,
            internals,
        };
    }
}

impl Type {
    /// Whether a value of the type has fields: a struct, a copy or one given
    /// an open type, whose fields are read with what it is given.
    pub fn has_fields(&self) -> bool {
        matches!(self, Type::Struct { .. } | Type::Named { .. })
    }

    /// What a name after `.` can mean on the type (docs/feature-front.md,
    /// 5.1): its fields, for a struct. A type with members can also have
    /// functions with it as owner.
    pub fn members(&self) -> Option<Members<'_>> {
        match self {
            Type::Struct {
                definition,
                fields,
                internals,
                ..
            } => Some(Members {
                definition: *definition,
                fields,
                offsets: &internals.offsets,
            }),
            _ => None,
        }
    }
}
