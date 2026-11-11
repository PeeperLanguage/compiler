//! What `as` gives on constant values.

use bigint::Limbs;
use model::constant::Value;
use model::semantic_types::Float;
use model::semantic_types::Type;

/// `value` of the type `from` as a value of the type `to`, which it converts
/// to (`Types::conversion`), as LLVM converts it: a wider integer keeps the
/// value (a signed one its sign), a narrower one keeps the low bits, `true`
/// is 1, a float rounds to the nearest value of its new type (ties to even),
/// and a float becomes an integer cut toward zero, saturated at the ends of
/// the integer's range, and 0 when NaN (as Rust's `as`).
pub fn convert(from: &Type, to: &Type, value: &Value) -> Value {
    match (from, to, value) {
        (Type::Integer(from), Type::Integer(to), Value::Integer(limbs)) => {
            Value::Integer(if to.bits >= from.bits {
                bigint::extend(from.bits, from.signed, limbs, to.bits)
            } else {
                Limbs::new(to.bits, limbs.as_slice())
            })
        }
        (Type::Bool, Type::Integer(to), &Value::Bool(value)) => {
            Value::Integer(Limbs::new(to.bits, &[u64::from(value)]))
        }
        // An `f32` value is already held as the `f64` it widens to.
        (Type::Float(Float::F32), Type::Float(Float::F64), Value::Float(_)) => value.clone(),
        (Type::Float(Float::F64), Type::Float(Float::F32), &Value::Float(bits)) => {
            Value::Float(f64::from(f64::from_bits(bits) as f32).to_bits())
        }
        (Type::Integer(from), Type::Float(float), Value::Integer(limbs)) => {
            let float_bits = if *float == Float::F32 { 32 } else { 64 };
            Value::Float(bigint::to_float(from.bits, from.signed, limbs, float_bits).to_bits())
        }
        (Type::Float(_), Type::Integer(to), &Value::Float(bits)) => {
            Value::Integer(bigint::from_float(to.bits, to.signed, f64::from_bits(bits)))
        }
        // The only constant pointer is `none`, null as every pointer type.
        (Type::RawPointer { .. }, Type::RawPointer { .. }, Value::Null) => Value::Null,
        _ => unreachable!("check converts no {from:?} value to {to:?}"),
    }
}
