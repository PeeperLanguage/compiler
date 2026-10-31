//! What a program that fails is told, the same at compile time and at run
//! time.

pub const DIVISION_BY_ZERO: &str = "division by zero";

/// What a shift of a value of `bits` bits by too far stops a program with.
pub fn shift_count_message(bits: u32) -> String {
    format!("a shift count must be from 0 to {}", bits - 1)
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
