//! What a program's outside sees, the same from a compiled program and from
//! one the interpreter runs.

/// The descriptors of a program's input, output and errors, as in `core`.
#[allow(dead_code)] // the runtime itself reads no stream by its number
pub const STDIN: i32 = 0;
pub const STDOUT: i32 = 1;
pub const STDERR: i32 = 2;

/// What a panic's text starts with.
pub const PANIC_PREFIX: &[u8] = b"panic: ";

/// The exit code of a program that panics, as in Rust.
pub const PANIC_EXIT_CODE: i32 = 101;
