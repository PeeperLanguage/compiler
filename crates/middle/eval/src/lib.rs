//! What Peeper's operators mean on constant values (`operators`,
//! `conversions`), and running a function at compile time (`interpreter`).
//! Both compute with `operations`, so check's folding and the interpreter
//! cannot disagree; LLVM must compute the same results
//! (tests/llvm_parity.rs).

pub mod conversions;
pub mod interpreter;
mod operations;
pub mod operators;

#[cfg(test)]
mod tests;
