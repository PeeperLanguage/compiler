//! The runtime functions a program calls, which `core` and the runtime
//! library spell too; `tests/runtime_agrees.rs` checks the three agree.

pub const ALLOC: &str = "peeper_rt_v1_alloc";
pub const REALLOC: &str = "peeper_rt_v1_realloc";
pub const FREE: &str = "peeper_rt_v1_free";
pub const WRITE: &str = "peeper_rt_v1_write";
pub const READ: &str = "peeper_rt_v1_read";
pub const EXIT: &str = "peeper_rt_v1_exit";
pub const PRINT_TEXT: &str = "peeper_rt_v1_print_text";
pub const PRINT_BYTES: &str = "peeper_rt_v1_print_bytes";
pub const PRINT_INT: &str = "peeper_rt_v2_print_int";
pub const PRINT_FLOAT: &str = "peeper_rt_v1_print_float";
pub const PRINT_POINTER: &str = "peeper_rt_v1_print_pointer";
pub const FLUSH: &str = "peeper_rt_v1_flush";

/// How C passes a value to or from a runtime function.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Carrier {
    I32,
    Usize, // as wide as the target's pointers
    F64,
    Pointer,
}

/// The parameters and result of a runtime function the backend calls by
/// itself, with no `core` declaration to read them from.
#[derive(Debug)]
pub struct Signature {
    pub symbol: &'static str,
    pub parameters: &'static [Carrier],
    pub result: Option<Carrier>,
}

/// A runtime function the backend calls by itself.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Runtime {
    /// Writes a panic's text, as `core`'s `Write` does.
    PanicWrite,
    /// Ends a panicking program, as `core`'s `Exit` does.
    PanicExit,
}

impl Runtime {
    pub fn signature(self) -> &'static Signature {
        match self {
            Runtime::PanicWrite => &Signature {
                symbol: WRITE,
                parameters: &[Carrier::I32, Carrier::Pointer, Carrier::I32],
                result: Some(Carrier::I32),
            },
            Runtime::PanicExit => &Signature {
                symbol: EXIT,
                parameters: &[Carrier::I32],
                result: None,
            },
        }
    }
}
