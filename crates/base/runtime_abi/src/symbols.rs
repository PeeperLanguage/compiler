//! The runtime functions a program calls, which `core` and the runtime
//! library spell too; `tests/runtime_agrees.rs` checks the three agree.

pub const ALLOC: &str = "peeper_rt_v1_alloc";
pub const REALLOC: &str = "peeper_rt_v1_realloc";
pub const FREE: &str = "peeper_rt_v1_free";
pub const WRITE: &str = "peeper_rt_v1_write";
pub const READ: &str = "peeper_rt_v1_read";
pub const EXIT: &str = "peeper_rt_v1_exit";

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
    /// `print` of a `&cstr`, null or not.
    PrintText,
    /// `print` of a `&str`: its bytes and its length.
    PrintBytes,
    /// `print` of an integer: whether it is signed, its count of 64-bit
    /// limbs, and the limbs, least significant first.
    PrintInt,
    /// `print` of a float: its value and its width in bits.
    PrintFloat,
    PrintPointer,
    /// Writes out what `print` has kept back.
    Flush,
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
            Runtime::PrintText => &Signature {
                symbol: "peeper_rt_v1_print_text",
                parameters: &[Carrier::Pointer],
                result: None,
            },
            Runtime::PrintBytes => &Signature {
                symbol: "peeper_rt_v1_print_bytes",
                parameters: &[Carrier::Pointer, Carrier::Usize],
                result: None,
            },
            Runtime::PrintInt => &Signature {
                symbol: "peeper_rt_v2_print_int",
                parameters: &[Carrier::I32, Carrier::I32, Carrier::Pointer],
                result: None,
            },
            Runtime::PrintFloat => &Signature {
                symbol: "peeper_rt_v1_print_float",
                parameters: &[Carrier::F64, Carrier::I32],
                result: None,
            },
            Runtime::PrintPointer => &Signature {
                symbol: "peeper_rt_v1_print_pointer",
                parameters: &[Carrier::Pointer],
                result: None,
            },
            Runtime::Flush => &Signature {
                symbol: "peeper_rt_v1_flush",
                parameters: &[],
                result: None,
            },
        }
    }
}
