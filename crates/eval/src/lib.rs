//! What Peeper's operators mean on constant values, and running a function at
//! compile time (`interpreter`). Check folds with these functions and the
//! interpreter runs with them, so the two cannot disagree; LLVM must compute
//! the same results (tests/llvm_parity.rs). The integer
//! arithmetic itself is the `bigint` crate's.
//!
//! Integers have any width from 1 to 2^23 bits. Arithmetic wraps to the width,
//! as the language's fixtures expect (`127i8 + 1i8 == -128i8`); division rounds
//! toward zero and `MIN / -1` is `MIN`, as the runtime defines it. Division by
//! zero and out-of-range shifts trap at run time, so they have no value here.
//!
//! Floats follow IEEE 754 in their own precision, as LLVM's `fadd`, `fsub`,
//! `fmul`, `fdiv`, `frem` and `fneg` do: an `f32` operation computes in `f32`.
//! Division by zero gives an infinity. `==` is true only for ordered equal
//! values and `!=` is its negation (`fcmp oeq` and `une`); `<` and the others
//! are false when either side is NaN. Which NaN an operation gives is not
//! specified, as in LLVM.

use std::cmp::Ordering;
use std::ops::Add;
use std::ops::Div;
use std::ops::Mul;
use std::ops::Rem;
use std::ops::Sub;

use bigint::Limbs;
use model::constant::Value;
use model::operators::BinaryOp;
use model::operators::UnaryOp;
use model::semantic_types::Float;
use model::semantic_types::Integer;
use model::semantic_types::Type;

pub mod interpreter;

/// Why an operation gives no value at compile time.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ValueError {
    DivisionByZero,
    /// A shift by a negative count or by the shifted type's width or more.
    ShiftOutOfRange,
}

/// What a program that hits `trap` on a left operand of `left_type` is told,
/// at compile time and at run time.
pub fn trap_message(trap: ValueError, left_type: &Type) -> String {
    match (trap, left_type) {
        (ValueError::ShiftOutOfRange, Type::Integer(shifted)) => {
            format!("a shift count must be from 0 to {}", shifted.bits - 1)
        }
        _ => "division by zero".to_string(),
    }
}

/// `left op right`. Both operands have one type, except that a shift count
/// has its own integer type (`value << count`). Check only gives an operator
/// operands it accepts.
pub fn binary(
    op: BinaryOp,
    left_type: &Type,
    left: &Value,
    right_type: &Type,
    right: &Value,
) -> Result<Value, ValueError> {
    debug_assert!(
        matches!(op, BinaryOp::ShiftLeft | BinaryOp::ShiftRight) || left_type == right_type,
        "check gives `{op:?}` operands of one type"
    );
    match (left_type, left, right_type, right) {
        (
            Type::Integer(integer),
            Value::Integer(left),
            Type::Integer(count_type),
            Value::Integer(right),
        ) => integer_binary(op, *integer, left, *count_type, right),
        (Type::Float(float), Value::Float(left), _, Value::Float(right)) => {
            let (left, right) = (f64::from_bits(*left), f64::from_bits(*right));
            Ok(match float {
                Float::F32 => float_binary(op, left as f32, right as f32),
                Float::F64 => float_binary(op, left, right),
            })
        }
        (Type::Bool, Value::Bool(left), Type::Bool, Value::Bool(right)) => {
            Ok(Value::Bool(match op {
                BinaryOp::LogicalAnd => *left && *right,
                BinaryOp::LogicalOr => *left || *right,
                BinaryOp::EqualTo => left == right,
                BinaryOp::NotEqualTo => left != right,
                _ => unreachable!("check gives `{op:?}` no bool operands"),
            }))
        }
        // The only constant pointer is `none`.
        (Type::RawPointer { .. }, Value::Null, _, Value::Null) => {
            Ok(Value::Bool(op == BinaryOp::EqualTo))
        }
        _ => {
            unreachable!("check gives `{op:?}` operands of types {left_type:?} and {right_type:?}")
        }
    }
}

/// `op value`, where the operand has the type `operand_type`.
pub fn unary(op: UnaryOp, operand_type: &Type, value: &Value) -> Result<Value, ValueError> {
    match (op, operand_type, value) {
        (UnaryOp::LogicalNot, Type::Bool, Value::Bool(value)) => Ok(Value::Bool(!value)),
        (UnaryOp::Negate, Type::Integer(integer), Value::Integer(value)) => {
            Ok(Value::Integer(bigint::negate(integer.bits, value)))
        }
        // Flipping the sign bit negates either precision, NaN and zero included.
        (UnaryOp::Negate, Type::Float(_), Value::Float(bits)) => Ok(Value::Float(bits ^ 1 << 63)),
        (UnaryOp::BitNot, Type::Integer(integer), Value::Integer(value)) => {
            Ok(Value::Integer(bigint::not(integer.bits, value)))
        }
        _ => unreachable!("check gives `{op:?}` an operand of type {operand_type:?}"),
    }
}

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

/// The count of a shift of a `shifted` value by `count`, of the type
/// `count_type`: below the shifted type's width, or out of range.
fn shift_count(shifted: Integer, count_type: Integer, count: &Limbs) -> Result<u32, ValueError> {
    bigint::to_u64(count_type.bits, count_type.signed, count)
        .filter(|&count| count < u64::from(shifted.bits))
        .map(|count| count as u32)
        .ok_or(ValueError::ShiftOutOfRange)
}

/// Why `op` traps whatever its left operand, of the type `left_type`, is,
/// with the right operand `right`, of the type `right_type`: a division by
/// zero or a shift count out of range. Both depend on the right operand
/// only, so `op` is tried on a zero left one.
pub fn known_trap(
    op: BinaryOp,
    left_type: &Type,
    right_type: &Type,
    right: &Value,
) -> Option<ValueError> {
    let Type::Integer(integer) = left_type else {
        return None; // float division gives an infinity or NaN
    };
    // The only operators that trap; skipping the others keeps folding cheap.
    if !matches!(
        op,
        BinaryOp::Divide | BinaryOp::Remainder | BinaryOp::ShiftLeft | BinaryOp::ShiftRight
    ) {
        return None;
    }
    let zero = Value::Integer(Limbs::new(integer.bits, &[]));
    binary(op, left_type, &zero, right_type, right).err()
}

fn integer_binary(
    op: BinaryOp,
    integer: Integer,
    left: &Limbs,
    count_type: Integer,
    right: &Limbs,
) -> Result<Value, ValueError> {
    let Integer { signed, bits } = integer;
    let compare = |accept: fn(Ordering) -> bool| {
        Ok(Value::Bool(accept(bigint::compare(
            bits, signed, left, right,
        ))))
    };
    let result = match op {
        BinaryOp::Add => bigint::add(bits, left, right),
        BinaryOp::Subtract => bigint::subtract(bits, left, right),
        BinaryOp::Multiply => bigint::multiply(bits, left, right),
        BinaryOp::Divide => {
            bigint::divide(bits, signed, left, right).ok_or(ValueError::DivisionByZero)?
        }
        BinaryOp::Remainder => {
            bigint::remainder(bits, signed, left, right).ok_or(ValueError::DivisionByZero)?
        }
        BinaryOp::ShiftLeft | BinaryOp::ShiftRight => {
            let count = shift_count(integer, count_type, right)?;
            match op {
                BinaryOp::ShiftLeft => bigint::shift_left(bits, left, count),
                _ => bigint::shift_right(bits, signed, left, count),
            }
        }
        BinaryOp::BitAnd => bigint::bit_and(bits, left, right),
        BinaryOp::BitOr => bigint::bit_or(bits, left, right),
        BinaryOp::BitXor => bigint::bit_xor(bits, left, right),
        BinaryOp::EqualTo => return Ok(Value::Bool(left == right)),
        BinaryOp::NotEqualTo => return Ok(Value::Bool(left != right)),
        BinaryOp::LessThan => return compare(Ordering::is_lt),
        BinaryOp::LessOrEqual => return compare(Ordering::is_le),
        BinaryOp::GreaterThan => return compare(Ordering::is_gt),
        BinaryOp::GreaterOrEqual => return compare(Ordering::is_ge),
        BinaryOp::LogicalAnd | BinaryOp::LogicalOr => {
            unreachable!("check gives `{op:?}` no integers")
        }
    };
    Ok(Value::Integer(result))
}

/// `left op right` in the precision of `T` (`f32` or `f64`).
fn float_binary<T>(op: BinaryOp, left: T, right: T) -> Value
where
    T: Copy + PartialOrd + Into<f64> + Add<Output = T> + Sub<Output = T>,
    T: Mul<Output = T> + Div<Output = T> + Rem<Output = T>,
{
    let result = match op {
        BinaryOp::Add => left + right,
        BinaryOp::Subtract => left - right,
        BinaryOp::Multiply => left * right,
        BinaryOp::Divide => left / right,
        BinaryOp::Remainder => left % right, // fmod, as LLVM's frem
        BinaryOp::EqualTo => return Value::Bool(left == right),
        BinaryOp::NotEqualTo => return Value::Bool(left != right),
        BinaryOp::LessThan => return Value::Bool(left < right),
        BinaryOp::LessOrEqual => return Value::Bool(left <= right),
        BinaryOp::GreaterThan => return Value::Bool(left > right),
        BinaryOp::GreaterOrEqual => return Value::Bool(left >= right),
        _ => unreachable!("check gives `{op:?}` no float operands"),
    };
    Value::Float(result.into().to_bits())
}

#[cfg(test)]
mod tests;
