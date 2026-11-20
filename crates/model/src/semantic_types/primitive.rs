//! The small types: `bool`, floats, `str`, `cstr`, `void` and the type of
//! an error. Their row in `builtin.rs` says nearly all they need; what more
//! they need is here, which is a float's register class.

use crate::mir;

use super::Float;

impl Float {
    /// The register class that holds a float of this type.
    pub fn class(self) -> mir::Class {
        match self {
            Float::F32 => mir::Class::F32,
            Float::F64 => mir::Class::F64,
        }
    }
}
