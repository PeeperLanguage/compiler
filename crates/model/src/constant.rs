//! Constant values. What Peeper's operators mean on them is the `eval`
//! crate's.

use bigint::Limbs;

use crate::semantic_types::TypeId;

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub enum Value {
    Bool(bool),
    /// Read through the integer's own type: the same bits are 255 as `u8` and
    /// -1 as `i8`.
    Integer(Limbs),
    /// The value's bits as an `f64`; an `f32` value widened, which is exact.
    /// Bits, not an `f64`, so that every value equals itself, NaN included.
    Float(u64),
    /// The bytes of a string, such as a `c"..."` literal's (a `cstr`), without
    /// the zero byte that ends a C string.
    Bytes(Box<[u8]>),
    /// The null address: `none` as a raw pointer.
    Null,
}

/// A constant's type and value.
#[derive(Clone, Debug)]
pub struct Constant {
    pub ty: TypeId,
    pub value: Value,
}

/// The bits of an integer in full: decimal when they fit one limb, else hex
/// of every limb, so a dump never narrows a wide value.
pub fn integer_text(limbs: &Limbs) -> String {
    match limbs.as_slice() {
        [low, high @ ..] if high.iter().all(|&limb| limb == 0) => low.to_string(),
        wide => {
            let hex: Vec<String> = wide
                .iter()
                .rev()
                .map(|limb| format!("{limb:016x}"))
                .collect();
            format!("0x{}", hex.concat())
        }
    }
}
