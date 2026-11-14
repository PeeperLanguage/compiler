//! What Peeper's operators and `as` give on constant values, folded while
//! checking. Each operation is computed with `eval::operations`, the
//! interpreter's own, so a folded value and a run one agree; the MIR
//! operation of each operator is lowering's (`lower::arithmetic`). Only a
//! shift count is read two ways: the folder reads it as its own type
//! (`operators::shift_count`), the interpreter as lowering converted it.

pub mod conversions;
pub mod operators;

#[cfg(test)]
mod tests;
