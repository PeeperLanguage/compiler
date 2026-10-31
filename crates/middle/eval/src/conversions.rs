//! What `as` gives on constant values.

use bigint::Limbs;
use model::constant::Value;
use model::semantic_types::Float;
use model::semantic_types::Integer;
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
            // Rounding the highest 64 bits once rounds the whole value;
            // scaling them back by a power of two is exact, or infinite.
            let (negative, top, below) = bigint::leading_bits(from.bits, from.signed, limbs);
            let magnitude = match float {
                Float::F32 => f64::from((f64::from(top as f32) * power_of_two(below)) as f32),
                Float::F64 => top as f64 * power_of_two(below),
            };
            Value::Float(if negative { -magnitude } else { magnitude }.to_bits())
        }
        (Type::Float(_), Type::Integer(to), &Value::Float(bits)) => {
            Value::Integer(saturated_integer(f64::from_bits(bits), *to))
        }
        // The only constant pointer is `none`, null as every pointer type.
        (Type::RawPointer { .. }, Type::RawPointer { .. }, Value::Null) => Value::Null,
        _ => unreachable!("check converts no {from:?} value to {to:?}"),
    }
}

/// The floats that an integer of the type `integer` holds when cut toward
/// zero lie strictly between these two: one at or below the first becomes
/// the type's lowest value, one at or above the second its highest. Each is
/// a power of two (or 0), infinite when too large for an `f64`.
fn float_range(integer: Integer) -> (f64, f64) {
    let Integer { signed, bits } = integer;
    if signed {
        (-power_of_two(bits - 1), power_of_two(bits - 1))
    } else {
        (0.0, power_of_two(bits))
    }
}

/// `value` as an integer of the type `integer`: cut toward zero, the
/// nearest end of the type's range when outside it (`float_range`), and 0
/// for NaN.
fn saturated_integer(value: f64, integer: Integer) -> Limbs {
    let Integer { signed, bits } = integer;
    let zero = Limbs::new(bits, &[]);
    let (low, high) = float_range(integer);
    // The signed minimum is the sign bit alone; each maximum is the
    // complement of the minimum.
    let lowest = if signed {
        bigint::shift_left(bits, &Limbs::new(bits, &[1]), bits - 1)
    } else {
        zero.clone()
    };
    if value.is_nan() {
        return zero;
    }
    if value <= low {
        return lowest;
    }
    if value >= high {
        return bigint::not(bits, &lowest);
    }
    let whole = value.trunc();
    if whole == 0.0 {
        return zero; // also -0.0
    }
    // A whole number from 1 up is normal: its 53-bit significand times a
    // power of two, inside the type's width.
    let float_bits = whole.abs().to_bits();
    let significand = float_bits & ((1 << 52) - 1) | 1 << 52;
    let exponent = (float_bits >> 52) as i32 - 1075;
    let magnitude = if exponent < 0 {
        Limbs::new(bits, &[significand >> -exponent])
    } else {
        bigint::shift_left(bits, &Limbs::new(bits, &[significand]), exponent as u32)
    };
    if whole < 0.0 {
        bigint::negate(bits, &magnitude)
    } else {
        magnitude
    }
}

/// 2^`exponent` as an `f64`, infinity when it is too large for one.
fn power_of_two(exponent: u32) -> f64 {
    match exponent {
        0..1024 => f64::from_bits(u64::from(exponent + 1023) << 52),
        _ => f64::INFINITY,
    }
}
