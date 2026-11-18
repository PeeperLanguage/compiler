//! How values are laid out in memory for the target: each type's size and
//! alignment, where a struct's fields go, and how a value is held (`held`:
//! in a register, as two words, in memory, or not at all). The checker
//! rejects a type too big for the target with it; the backend places and
//! copies values with it, and LLVM sees only the bytes.

use crate::machine::Machine;
use crate::mir::Class;
use crate::semantic_types;
use crate::semantic_types::Float;
use crate::semantic_types::Type;
use crate::semantic_types::TypeId;
use crate::semantic_types::Types;

/// The size and alignment in bytes of a value of type `ty` in memory for
/// `target`: an integer is aligned as the next of 8, 16, 32, 64 and 128 bits
/// that holds it, a 64-bit one as `target` says, and wider ones as 128; its
/// size is rounded up to that. LLVM loads and stores a scalar itself, so a
/// scalar's must match LLVM's; a value kept in memory is bytes to LLVM, so
/// this alone says where its parts are. A size past `u64` is `u64::MAX`, more
/// than any machine's `largest_object`: the checker rejects such a type, so
/// no later phase lays it out.
#[deny(
    clippy::wildcard_enum_match_arm,
    clippy::match_wildcard_for_single_variants
)]
pub fn layout(types: &Types, ty: TypeId, target: &Machine) -> (u64, u64) {
    match &types[ty] {
        Type::Bool => (1, 1),
        Type::Integer(integer) => {
            let align: u64 = match integer.bits {
                1..=8 => 1,
                9..=16 => 2,
                17..=32 => 4,
                33..=64 => target.i64_align,
                _ => 16,
            };
            // The class's bytes: its own, rounded up to the alignment.
            let bytes = Class::integer(integer.bits).bytes(target.pointer_bytes());
            (bytes, align)
        }
        Type::Float(Float::F32) => (4, 4),
        Type::Float(Float::F64) => (8, target.i64_align),
        Type::CString | Type::Reference { .. } | Type::RawPointer { .. } => {
            let bytes = target.pointer_bytes();
            (bytes, bytes)
        }
        Type::Slice { .. }
        | Type::Str
        | Type::InterfaceReference { .. }
        | Type::Interface { .. } => {
            let bytes = target.pointer_bytes();
            (2 * bytes, bytes)
        }
        Type::Struct { internals, .. } => (internals.size, internals.align),
        Type::Range { element, step } => {
            let fields = semantic_types::ranges::range_fields(*element, *step).map(|(_, ty)| ty);
            let (_, size, align) = struct_layout(types, &fields, target);
            (size, align)
        }
        Type::Array { internals, .. } => {
            let (size, align) = layout(types, internals.base, target);
            (size.saturating_mul(internals.count), align)
        }
        Type::Invalid | Type::Void | Type::Function { .. } | Type::Named { .. } => {
            unreachable!("no value is laid out with this type")
        }
        Type::Alias { .. } => unreachable!("indexing looks through aliases"),
    }
}

/// How a value of a type is held while the program runs (`held`).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Held {
    /// Whole, as one number of this class.
    Register(Class),
    /// As two words: an address, then a length or a method table.
    TwoWords,
    /// In memory, as bytes: a struct, an array or a range. LLVM splits a
    /// value of such a type into one operation per part, so a big array as
    /// one value made clang slow, then crash; the interpreter copies such a
    /// value as its bytes.
    Memory,
    /// Not held: no value has the type, as it is an error's, nothing
    /// (`void`), or a function's, not yet a value.
    Nothing,
}

/// How a value of type `ty` is held while the program runs.
#[deny(
    clippy::wildcard_enum_match_arm,
    clippy::match_wildcard_for_single_variants
)]
pub fn held(types: &Types, ty: TypeId) -> Held {
    match types[ty] {
        Type::Bool => Held::Register(Class::I8),
        Type::Integer(integer) => Held::Register(Class::integer(integer.bits)),
        Type::Float(float) => Held::Register(float.class()),
        Type::CString | Type::Reference { .. } | Type::RawPointer { .. } => {
            Held::Register(Class::Pointer)
        }
        Type::Slice { .. }
        | Type::Str
        | Type::InterfaceReference { .. }
        | Type::Interface { .. } => Held::TwoWords,
        Type::Struct { .. } | Type::Array { .. } | Type::Range { .. } => Held::Memory,
        Type::Invalid | Type::Void | Type::Function { .. } => Held::Nothing,
        Type::Named { .. } => unreachable!("no value has a type not checked yet"),
        Type::Alias { .. } => unreachable!("indexing looks through aliases"),
    }
}

/// Field `index` of the struct, range or `str` type `ty`: where it is from
/// the value's start, in bytes, and its type. A struct's offsets are kept
/// (`StructInternals`); a range's two or three fields are laid out here; a
/// `str` is its bytes' address, then its length.
pub fn field_placed(types: &Types, ty: TypeId, index: usize, target: &Machine) -> (u64, TypeId) {
    match &types[ty] {
        ty if let Some(members) = ty.members() => (members.offsets[index], members.fields[index]),
        Type::Str => [(0, TypeId::RAWPTR), (target.pointer_bytes(), types.usize)][index],
        Type::Range { element, step } => {
            let fields = semantic_types::ranges::range_fields(*element, *step).map(|(_, ty)| ty);
            let (offsets, ..) = struct_layout(types, &fields, target);
            (offsets[index], fields[index])
        }
        _ => unreachable!("only a struct, a range and a `str` have fields"),
    }
}

/// The offset of each of `fields` in a struct holding them in order, and the
/// struct's size and alignment, in bytes (`layout`): each field at the next
/// multiple of its alignment, the size rounded up to the largest.
pub fn struct_layout(types: &Types, fields: &[TypeId], target: &Machine) -> (Vec<u64>, u64, u64) {
    let (mut offsets, mut end, mut align) = (Vec::with_capacity(fields.len()), 0u64, 1);
    for &field in fields {
        let (size, field_align) = layout(types, field, target);
        let offset = end
            .checked_next_multiple_of(field_align)
            .unwrap_or(u64::MAX);
        offsets.push(offset);
        end = offset.saturating_add(size);
        align = align.max(field_align);
    }
    let size = end.checked_next_multiple_of(align).unwrap_or(u64::MAX);
    (offsets, size, align)
}

#[cfg(test)]
mod tests {
    use crate::machine::Machine;
    use crate::semantic_types::TypeId;
    use crate::semantic_types::Types;

    use super::struct_layout;

    /// Each struct's field offsets and size match what LLVM gives on x86-64
    /// and AArch64, and with 32-bit pointers on i686 (measured with
    /// `getelementptr` folded by clang `-O1`, `-m32` for i686).
    #[test]
    fn struct_layout_matches_llvm() {
        let mut types = Types::new(64);
        let mut integer = |name: &str| types.builtin(name).unwrap();
        let (i8, i24, i40, i128, i200) = (
            integer("i8"),
            integer("i24"),
            integer("i40"),
            integer("i128"),
            integer("i200"),
        );
        let layout = |fields: &[TypeId]| {
            let (offsets, size, _) =
                struct_layout(&types, fields, Machine::named("x86_64-linux").unwrap());
            (offsets, size)
        };
        assert_eq!(layout(&[TypeId::I32, TypeId::I64]), (vec![0, 8], 16));
        assert_eq!(
            layout(&[TypeId::BOOL, i128, TypeId::I32]),
            (vec![0, 16, 32], 48)
        );
        assert_eq!(layout(&[i8, i24, i8]), (vec![0, 4, 8], 12));
        assert_eq!(layout(&[i8, i200, i8]), (vec![0, 16, 48], 64));
        assert_eq!(layout(&[i8, i40]), (vec![0, 8], 16));
        assert_eq!(
            layout(&[i8, TypeId::F64, TypeId::CSTR]),
            (vec![0, 8, 16], 24)
        );
        assert_eq!(layout(&[]), (vec![], 0));
        let i686 = |fields: &[TypeId]| {
            let (offsets, size, _) =
                struct_layout(&types, fields, Machine::named("i686-linux").unwrap());
            (offsets, size)
        };
        assert_eq!(i686(&[TypeId::I32, TypeId::I64]), (vec![0, 4], 12));
        assert_eq!(
            i686(&[TypeId::BOOL, i128, TypeId::I32]),
            (vec![0, 16, 32], 48)
        );
        assert_eq!(i686(&[i8, i24, i8]), (vec![0, 4, 8], 12));
        assert_eq!(i686(&[i8, i200, i8]), (vec![0, 16, 48], 64));
        assert_eq!(i686(&[i8, i40]), (vec![0, 4], 12));
        assert_eq!(i686(&[i8, TypeId::F64, TypeId::CSTR]), (vec![0, 4, 12], 16));
    }
}
