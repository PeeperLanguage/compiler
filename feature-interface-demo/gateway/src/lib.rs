//! The core, and the agreed interface between it and every feature.
//!
//! The core is the pipeline up to the shared MIR: the lexer and parsing
//! engine, names and scopes, the phase order, the type table, conversion
//! where a type is expected, control flow, THIR, and lowering THIR to MIR.
//! It never names a feature. A feature (integers, structs, references ...)
//! brings parse rules; each rule puts a check on its syntax nodes, each
//! check puts a kind on its THIR nodes, and each type a kind on its entry.
//! A kind is a static of plain functions with every field required, so a
//! missing answer is a build error, and there is no list of features to
//! forget a case in.

pub mod check;
pub mod control;
pub mod feature;
pub mod lower;
pub mod syntax;
pub mod thir;
pub mod tree;
pub mod types;
