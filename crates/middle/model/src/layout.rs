//! How values are laid out in memory, as LLVM lays them out for the target:
//! each type's size and alignment. The checker rejects a type too big for
//! the target with it; the backend places and copies values with it.

use crate::machine::Machine;
use crate::semantic_types::Float;
use crate::semantic_types::Type;
use crate::semantic_types::TypeId;
use crate::semantic_types::Types;
use crate::semantic_types::range_fields;

/// The size and alignment in bytes of a value of type `ty` in memory, as LLVM
/// lays it out for `target`: an integer is aligned as the next of 8, 16, 32,
/// 64 and 128 bits that holds it, a 64-bit one as `target` says, and wider
/// ones as 128; its size is rounded up to that. Debug info places each field
/// with it, and a copy of a value kept in memory copies its size, so it must
/// match LLVM's. A size past `u64` is `u64::MAX`, more than any machine's
/// `largest_object`: the checker rejects such a type, so no later phase lays
/// it out.
pub fn layout(types: &Types, ty: TypeId, target: &Machine) -> (u64, u64) {
    match types[ty] {
        Type::Bool => (1, 1),
        Type::Integer(integer) => {
            let align: u64 = match integer.bits {
                1..=8 => 1,
                9..=16 => 2,
                17..=32 => 4,
                33..=64 => target.i64_align,
                _ => 16,
            };
            let bytes = u64::from(integer.bits.div_ceil(8));
            (bytes.next_multiple_of(align), align)
        }
        Type::Float(Float::F32) => (4, 4),
        Type::Float(Float::F64) => (8, target.i64_align),
        Type::CString | Type::Reference { .. } | Type::RawPointer { .. } => {
            let bytes = u64::from(target.pointer_bits / 8);
            (bytes, bytes)
        }
        Type::Slice { .. } => {
            let bytes = u64::from(target.pointer_bits / 8);
            (2 * bytes, bytes)
        }
        Type::Struct { ref fields, .. } => {
            let (_, size, align) = struct_layout(types, fields, target);
            (size, align)
        }
        Type::Range { element, step } => {
            let fields = range_fields(element, step);
            let (_, size, align) = struct_layout(types, &fields, target);
            (size, align)
        }
        Type::Array { element, length } => {
            let (size, align) = layout(types, element, target);
            (size.saturating_mul(length), align)
        }
        Type::Invalid | Type::Void | Type::Function { .. } | Type::Named(_) => {
            unreachable!("no value is laid out with this type")
        }
        Type::Alias { .. } => unreachable!("indexing looks through aliases"),
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
mod tests;
