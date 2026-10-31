//! What a program that fails is told, the same at compile time and at run
//! time.

use model::semantic_types::Type;

use crate::operators::ValueError;

/// What a program that hits `trap` on a left operand of `left_type` is told,
/// at compile time and at run time.
pub fn trap_message(trap: ValueError, left_type: &Type) -> String {
    match (trap, left_type) {
        (ValueError::ShiftOutOfRange, Type::Integer(shifted)) => {
            format!("a shift count must be from 0 to {}", shifted.bits - 1)
        }
        _ => "division by zero".to_string(),
    }
}

/// What an index outside an array of `length` elements stops a program with.
pub fn array_index_message(length: u64) -> String {
    format!("index out of bounds: the length is {length}")
}

/// What an index outside a slice's elements stops a program with.
pub const SLICE_INDEX_MESSAGE: &str = "index out of bounds of the slice";

/// What array parts that do not give `length` elements stop a program with.
pub fn array_parts_message(length: u64) -> String {
    format!("array literal needs exactly {length} values")
}
