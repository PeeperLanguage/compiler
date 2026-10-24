//! Constant values and what Peeper's operators mean on them. Check folds with
//! these functions, and the interpreter will run with them, so the two cannot
//! disagree; LLVM must compute the same results (tests/llvm_parity.rs). The
//! integer arithmetic itself is the `bigint` crate's.
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

use crate::operators::BinaryOp;
use crate::operators::UnaryOp;
use crate::semantic_types::Float;
use crate::semantic_types::Integer;
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

/// Why an operation gives no value at compile time.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ValueError {
    DivisionByZero,
    /// A shift by a negative count or by the shifted type's width or more.
    ShiftOutOfRange,
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
pub fn shift_count(
    shifted: Integer,
    count_type: Integer,
    count: &Limbs,
) -> Result<u32, ValueError> {
    bigint::to_u64(count_type.bits, count_type.signed, count)
        .filter(|&count| count < u64::from(shifted.bits))
        .map(|count| count as u32)
        .ok_or(ValueError::ShiftOutOfRange)
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
mod tests {
    use bigint::Limbs;

    use super::Value;
    use super::ValueError;
    use super::binary;
    use super::convert;
    use super::unary;
    use crate::operators::BinaryOp;
    use crate::operators::UnaryOp;
    use crate::semantic_types::Float;
    use crate::semantic_types::Integer;
    use crate::semantic_types::Type;

    fn int(signed: bool, bits: u32) -> Type {
        Type::Integer(Integer { signed, bits })
    }

    /// `value` in the type `ty`, sign-extended to every limb before wrapping.
    fn value(ty: &Type, value: i128) -> Value {
        let Type::Integer(integer) = ty else {
            unreachable!()
        };
        let fill = if value < 0 { u64::MAX } else { 0 };
        let mut limbs = vec![fill; integer.bits.div_ceil(64) as usize + 2];
        limbs[0] = value as u64;
        limbs[1] = (value >> 64) as u64;
        Value::Integer(Limbs::new(integer.bits, &limbs))
    }

    fn fold(op: BinaryOp, ty: &Type, left: i128, right: i128) -> Result<Value, ValueError> {
        binary(op, ty, &value(ty, left), ty, &value(ty, right))
    }

    #[test]
    fn arithmetic_wraps_like_the_fixtures() {
        let i8 = int(true, 8);
        assert_eq!(fold(BinaryOp::Add, &i8, 127, 1), Ok(value(&i8, -128)));
        assert_eq!(fold(BinaryOp::Subtract, &i8, -128, 1), Ok(value(&i8, 127)));
        assert_eq!(fold(BinaryOp::Multiply, &i8, 64, 2), Ok(value(&i8, -128)));
        assert_eq!(
            unary(UnaryOp::Negate, &i8, &value(&i8, -128)),
            Ok(value(&i8, -128))
        );
        assert_eq!(fold(BinaryOp::Divide, &i8, -128, -1), Ok(value(&i8, -128)));
        assert_eq!(fold(BinaryOp::Remainder, &i8, -128, -1), Ok(value(&i8, 0)));
        let i32 = int(true, 32);
        assert_eq!(fold(BinaryOp::Divide, &i32, -7, 3), Ok(value(&i32, -2)));
        assert_eq!(fold(BinaryOp::Remainder, &i32, -7, 3), Ok(value(&i32, -1)));
        let u8 = int(false, 8);
        assert_eq!(fold(BinaryOp::Add, &u8, 255, 1), Ok(value(&u8, 0)));
        assert_eq!(
            unary(UnaryOp::BitNot, &u8, &value(&u8, 0)),
            Ok(value(&u8, 255))
        );
        // The same bits read through their own type: 255 as u8, -1 as i8.
        assert_eq!(value(&u8, 255), value(&i8, -1));
    }

    #[test]
    fn values_that_cannot_be_folded() {
        let u8 = int(false, 8);
        let i8 = int(true, 8);
        assert_eq!(
            fold(BinaryOp::Divide, &u8, 1, 0),
            Err(ValueError::DivisionByZero)
        );
        assert_eq!(
            fold(BinaryOp::Remainder, &i8, 1, 0),
            Err(ValueError::DivisionByZero)
        );
        assert_eq!(
            fold(BinaryOp::ShiftLeft, &u8, 1, 8),
            Err(ValueError::ShiftOutOfRange)
        );
        assert_eq!(
            fold(BinaryOp::ShiftLeft, &i8, 1, -1),
            Err(ValueError::ShiftOutOfRange)
        );
        assert_eq!(fold(BinaryOp::ShiftLeft, &u8, 1, 7), Ok(value(&u8, 128)));
        // 1 << (255u8 + 1u8) from the fixtures: the count wraps to 0 first.
        let count = fold(BinaryOp::Add, &u8, 255, 1).unwrap();
        assert_eq!(
            binary(BinaryOp::ShiftLeft, &u8, &value(&u8, 1), &u8, &count),
            Ok(value(&u8, 1))
        );
    }

    #[test]
    fn shift_counts_have_their_own_type() {
        let (i64, u8, u16, u2) = (int(true, 64), int(false, 8), int(false, 16), int(false, 2));
        let shift = |op, ty: &Type, left, count_type: &Type, count| {
            binary(
                op,
                ty,
                &value(ty, left),
                count_type,
                &value(count_type, count),
            )
        };
        assert_eq!(
            shift(BinaryOp::ShiftLeft, &i64, 1, &u8, 3),
            Ok(value(&i64, 8))
        );
        assert_eq!(
            shift(BinaryOp::ShiftRight, &u8, 128, &u16, 2),
            Ok(value(&u8, 32))
        );
        assert_eq!(
            shift(BinaryOp::ShiftLeft, &u8, 1, &u2, 3),
            Ok(value(&u8, 8))
        );
        assert_eq!(
            shift(BinaryOp::ShiftLeft, &u8, 1, &u16, 8),
            Err(ValueError::ShiftOutOfRange)
        );
        assert_eq!(
            shift(BinaryOp::ShiftLeft, &u8, 1, &u16, 256),
            Err(ValueError::ShiftOutOfRange)
        );
    }

    #[test]
    fn widening_keeps_the_value() {
        let (i8, u8, i16, i256) = (int(true, 8), int(false, 8), int(true, 16), int(true, 256));
        assert_eq!(convert(&i8, &i16, &value(&i8, -1)), value(&i16, -1));
        assert_eq!(convert(&u8, &i16, &value(&u8, 255)), value(&i16, 255));
        assert_eq!(convert(&i8, &i256, &value(&i8, -128)), value(&i256, -128));
        let (f32, f64) = (Type::Float(Float::F32), Type::Float(Float::F64));
        assert_eq!(
            convert(&f32, &f64, &float(&f32, 0.1)),
            float(&f64, f64::from(0.1f32))
        );
    }

    fn float(ty: &Type, value: f64) -> Value {
        match ty {
            Type::Float(Float::F32) => Value::Float(f64::from(value as f32).to_bits()),
            _ => Value::Float(value.to_bits()),
        }
    }

    #[test]
    fn floats_compute_in_their_own_precision() {
        let (f32, f64) = (Type::Float(Float::F32), Type::Float(Float::F64));
        let fold =
            |op, ty: &Type, left, right| binary(op, ty, &float(ty, left), ty, &float(ty, right));
        // 0.1 + 0.2 rounds differently in single and double precision.
        assert_eq!(
            fold(BinaryOp::Add, &f32, 0.1, 0.2),
            Ok(float(&f32, f64::from(0.1f32 + 0.2f32)))
        );
        assert_eq!(
            fold(BinaryOp::Add, &f64, 0.1, 0.2),
            Ok(float(&f64, 0.1 + 0.2))
        );
        // From the fixtures: `5.5f32 % 2.0f32` and `1.0f32 / 0.0f32`.
        assert_eq!(
            fold(BinaryOp::Remainder, &f32, 5.5, 2.0),
            Ok(float(&f32, 1.5))
        );
        assert_eq!(
            fold(BinaryOp::Remainder, &f64, -5.5, 2.0),
            Ok(float(&f64, -1.5))
        );
        assert_eq!(
            fold(BinaryOp::Divide, &f32, 1.0, 0.0),
            Ok(float(&f32, f64::INFINITY))
        );
        assert_eq!(
            fold(BinaryOp::EqualTo, &f64, f64::NAN, f64::NAN),
            Ok(Value::Bool(false))
        );
        assert_eq!(
            fold(BinaryOp::NotEqualTo, &f64, f64::NAN, f64::NAN),
            Ok(Value::Bool(true))
        );
        assert_eq!(
            fold(BinaryOp::LessThan, &f64, f64::NAN, 1.0),
            Ok(Value::Bool(false))
        );
        assert_eq!(
            fold(BinaryOp::EqualTo, &f64, 0.0, -0.0),
            Ok(Value::Bool(true))
        );
        assert_eq!(
            unary(UnaryOp::Negate, &f32, &float(&f32, 0.0)),
            Ok(float(&f32, -0.0))
        );
        assert_ne!(
            float(&f64, 0.0),
            float(&f64, -0.0),
            "values keep their bits"
        );
    }

    #[test]
    fn bools() {
        let and = binary(
            BinaryOp::LogicalAnd,
            &Type::Bool,
            &Value::Bool(true),
            &Type::Bool,
            &Value::Bool(false),
        );
        assert_eq!(and, Ok(Value::Bool(false)));
        assert_eq!(
            unary(UnaryOp::LogicalNot, &Type::Bool, &Value::Bool(false)),
            Ok(Value::Bool(true))
        );
    }
}
