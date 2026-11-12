//! What Peeper's operators mean on constant values (`operators`,
//! `conversions`), and running a function at compile time (`interpreter`).
//! Both compute each operation with `operations`, so check's folding and
//! the interpreter agree on it. Only a shift count is read two ways: the
//! folder reads it as its own type (`operators::shift_count`), the
//! interpreter as lowering converted it. LLVM must compute the same results
//! (tests/llvm_parity.rs).

pub mod conversions;
pub mod interpreter;
mod operations;
pub mod operators;

#[cfg(test)]
mod tests;
