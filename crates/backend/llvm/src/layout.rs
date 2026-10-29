//! Which values are kept in memory; `model::layout` gives their size.

use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;

/// Whether a value of type `ty` is kept in memory, its LLVM value the
/// address there: a struct, an array or a range. LLVM splits a value of such a type
/// into one operation per part, so a big array as one value made clang slow,
/// then crash.
pub(crate) fn in_memory(types: &Types, ty: TypeId) -> bool {
    matches!(
        types[ty],
        Type::Struct { .. } | Type::Array { .. } | Type::Range { .. }
    )
}
