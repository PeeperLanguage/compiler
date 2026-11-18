//! The syntax tree the parser builds (`ast`) and the operator kinds it
//! names (`operators`). The checker reads the tree; `thir` reuses the
//! operator kinds. See docs/compiler-layout.md.

pub mod ast;
pub mod operators;
