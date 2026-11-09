//! What every phase may know about any type. A type kind computes its facts
//! once (`TypeKind::facts`), from its own detail and its parts' facts, and
//! the core stores them; no phase asks which feature a type belongs to.

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Facts {
    pub size: u64,
    pub align: u64,
    /// How a value is carried; the backends decide everything from this.
    pub carrier: Carrier,
    /// Whether using it by value copies it, else it moves.
    pub copies: bool,
    /// How many loan holders a value of it is: 1 for a reference or an
    /// owned interface, the sum of the fields for a struct.
    pub holders: usize,
}

impl Facts {
    /// The facts of no value: `void`, an empty struct, an error.
    pub const NOTHING: Facts = Facts {
        size: 0,
        align: 1,
        carrier: Carrier::Nothing,
        copies: true,
        holders: 0,
    };

    /// One integer register of `bytes`.
    pub const fn scalar(bytes: u64) -> Facts {
        Facts {
            size: bytes,
            align: bytes,
            carrier: Carrier::Scalar,
            copies: true,
            holders: 0,
        }
    }
}

/// How a value is carried. A `Scalar` is a register; anything else lives
/// in memory and is passed around by its address.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Carrier {
    Nothing,
    Scalar,
    /// Two pointer-sized words: an address, then a count (a slice) or a
    /// method table (an interface).
    Pair,
    Memory,
}

/// The machine a program is built for.
#[derive(Clone, Copy, Debug)]
pub struct Target {
    pub pointer_bytes: u64,
}
