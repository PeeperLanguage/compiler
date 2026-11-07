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

/// A runtime function the backend calls by itself, with no `core`
/// declaration to read its signature from.
#[derive(Debug)]
pub struct Signature {
    pub symbol: &'static str,
    pub parameters: &'static [Carrier],
    pub result: Option<Carrier>,
}

/// Writes a panic's text, as `core`'s `Write` does.
pub const PANIC_WRITE: Signature = Signature {
    symbol: WRITE,
    parameters: &[Carrier::I32, Carrier::Pointer, Carrier::I32],
    result: Some(Carrier::I32),
};
/// Ends a panicking program, as `core`'s `Exit` does.
pub const PANIC_EXIT: Signature = Signature {
    symbol: EXIT,
    parameters: &[Carrier::I32],
    result: None,
};
/// `print` of a `&cstr`, null or not.
pub const PRINT_TEXT: Signature = Signature {
    symbol: "peeper_rt_v1_print_text",
    parameters: &[Carrier::Pointer],
    result: None,
};
/// `print` of a `&str`: its bytes and its length.
pub const PRINT_BYTES: Signature = Signature {
    symbol: "peeper_rt_v1_print_bytes",
    parameters: &[Carrier::Pointer, Carrier::Usize],
    result: None,
};
/// `print` of an integer: whether it is signed, its count of 64-bit limbs,
/// and the limbs, least significant first.
pub const PRINT_INT: Signature = Signature {
    symbol: "peeper_rt_v2_print_int",
    parameters: &[Carrier::I32, Carrier::I32, Carrier::Pointer],
    result: None,
};
/// `print` of a float: its value and its width in bits.
pub const PRINT_FLOAT: Signature = Signature {
    symbol: "peeper_rt_v1_print_float",
    parameters: &[Carrier::F64, Carrier::I32],
    result: None,
};
pub const PRINT_POINTER: Signature = Signature {
    symbol: "peeper_rt_v1_print_pointer",
    parameters: &[Carrier::Pointer],
    result: None,
};
/// Writes out what `print` has kept back.
pub const FLUSH: Signature = Signature {
    symbol: "peeper_rt_v1_flush",
    parameters: &[],
    result: None,
};
