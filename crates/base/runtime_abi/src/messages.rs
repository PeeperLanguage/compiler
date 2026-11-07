//! What a program that fails is told, the same at compile time and at run
//! time.

use std::path::Path;

use source::SourceFile;
use source::Span;

use crate::abi::PANIC_PREFIX;

/// What a program prints when it stops with `message` at `span` of `file`,
/// the file's path read from the project's `root`.
pub fn panic_text(root: &Path, file: &SourceFile, message: &[u8], span: Span) -> Vec<u8> {
    let (line, column) = file.position(span.start as usize);
    let mut text = PANIC_PREFIX.to_vec();
    text.extend_from_slice(message);
    let path = Path::new(&file.path);
    let path = path.strip_prefix(root).unwrap_or(path);
    let place = format!("\n  --> {}:{line}:{column}\n", path.display());
    text.extend_from_slice(place.as_bytes());
    text
}

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

/// What slicing from after where the slice ends stops a program with.
pub const SLICE_ORDER_MESSAGE: &str = "slice starts after it ends";

/// What slicing past the end of what is sliced stops a program with.
pub const SLICE_BOUNDS_MESSAGE: &str = "slice out of bounds";

/// What a range walked with a step of zero stops a program with.
pub const RANGE_STEP_MESSAGE: &str = "range step is zero";

/// What array parts that do not give `length` elements stop a program with.
pub fn array_parts_message(length: u64) -> String {
    format!("array literal needs exactly {length} values")
}
