//! Constant values. What Peeper's operators mean on them is the `eval`
//! crate's.

use std::fmt;
use std::fmt::Display;

use bigint::Limbs;

use crate::semantic_types::Float;
use crate::semantic_types::Type;
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

/// `value`, of the type `ty`, as Peeper text: an integer in decimal, read
/// through its type's sign, when 128 bits hold it, else every limb in hex
/// (`integer_text`); a float as the shortest text that reads back as it, in
/// its own precision when it holds an `f32` exactly (a value that does not
/// shows in full); a string as a `c"..."` literal; `none` for the null
/// address.
pub fn value_text<'v>(value: &'v Value, ty: &'v Type) -> impl Display + 'v {
    fmt::from_fn(move |out| match (value, ty) {
        (Value::Bool(value), _) => write!(out, "{value}"),
        (Value::Null, _) => out.write_str("none"),
        (Value::Bytes(bytes), _) => write!(out, "c\"{}\"", bytes.escape_ascii()),
        (Value::Float(bits), Type::Float(Float::F32))
            if f64::from(f64::from_bits(*bits) as f32) == f64::from_bits(*bits) =>
        {
            write!(out, "{}", f64::from_bits(*bits) as f32)
        }
        (Value::Float(bits), _) => write!(out, "{}", f64::from_bits(*bits)),
        (Value::Integer(limbs), Type::Integer(integer)) => {
            let value = match integer.bits {
                ..=128 => bigint::extend(integer.bits, integer.signed, limbs, 128),
                _ => limbs.clone(),
            };
            let [low, high, rest @ ..] = value.as_slice() else {
                unreachable!("an integer constant has at least two limbs")
            };
            let low = *low as u128 | (*high as u128) << 64;
            if !rest.iter().all(|&limb| limb == 0) {
                write!(out, "{}", integer_text(&value))
            } else if integer.signed && integer.bits <= 128 {
                write!(out, "{}", low as i128)
            } else {
                write!(out, "{low}")
            }
        }
        (Value::Integer(_), _) => unreachable!("an integer constant has an integer type"),
    })
}
