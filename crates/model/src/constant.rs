//! Constant values, as the MIR and a compile-time run hold them; a
//! constant with its type is `semantic_types::Constant`.

use bigint::Limbs;

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub enum Value {
    Bool(bool),
    /// Read through the integer's own type: the same bits are 255 as `u8` and
    /// -1 as `i8`.
    Integer(Limbs),
    /// The value's bits as an `f64`; an `f32` value widened, which is exact.
    /// Bits, not an `f64`, so that every value equals itself, NaN included.
    Float(u64),
    /// The bytes of a string, such as a `c"..."` literal's (a `&cstr`), without
    /// the zero byte that ends a C string.
    Bytes(Box<[u8]>),
    /// The null address: `none` as a raw pointer.
    Null,
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
