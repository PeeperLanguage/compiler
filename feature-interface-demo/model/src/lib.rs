//! The data every phase after checking reads: where code is written, type
//! ids, each type's facts and the shared MIR. The path checks, cleanup, the
//! interpreter and the LLVM writer depend on this crate only, so none of
//! them can name a feature.

pub mod facts;
pub mod mir;
pub mod span;
pub mod types;
