//! What a program's outside sees, the same from a compiled program and from
//! one the interpreter runs.

/// The descriptors of a program's output and errors, as in `core`.
pub const STDOUT: i32 = 1;
pub const STDERR: i32 = 2;

/// The exit code of a program that panics, as in Rust.
pub const PANIC_EXIT_CODE: i32 = 101;
