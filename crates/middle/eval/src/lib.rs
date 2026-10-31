//! What Peeper's operators mean on constant values (`operators`,
//! `conversions`), and running a function at compile time (`interpreter`).
//! Check folds with these functions and the interpreter runs with them, so
//! the two cannot disagree; LLVM must compute the same results
//! (tests/llvm_parity.rs). The integer arithmetic itself is the `bigint`
//! crate's.

pub mod conversions;
pub mod interpreter;
pub mod operators;

#[cfg(test)]
mod tests;
