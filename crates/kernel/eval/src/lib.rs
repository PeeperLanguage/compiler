//! Running a function's MIR at compile time (`interpreter`), and what each
//! MIR operation gives on register values (`operations`), which check's
//! constant folder computes with too (`check::fold`), so the two agree.
//! LLVM must compute the same results (check's tests/llvm_parity.rs).

pub mod interpreter;
pub mod operations;
