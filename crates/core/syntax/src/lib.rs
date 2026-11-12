//! The syntax tree the parser builds (`ast`) and the operator kinds it
//! names (`operators`). The checker reads the tree; `thir` reuses the
//! operator kinds. See docs/feature-front.md, section 9.

pub mod ast;
pub mod operators;
