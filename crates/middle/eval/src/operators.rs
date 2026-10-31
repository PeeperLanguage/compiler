//! What each operator gives on constant values.
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
use runtime_abi::messages::DIVISION_BY_ZERO;
use runtime_abi::messages::shift_count_message;

/// Why an operation gives no value at compile time.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ValueError {
    DivisionByZero,
    /// A shift by a negative count or by the shifted type's width or more.
    ShiftOutOfRange,
}

/// What a program that hits `trap` on a left operand of `left_type` is told.
pub fn trap_message(trap: ValueError, left_type: &Type) -> String {
    match (trap, left_type) {
        (ValueError::ShiftOutOfRange, Type::Integer(shifted)) => shift_count_message(shifted.bits),
        _ => DIVISION_BY_ZERO.to_string(),
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
                // Only a range's loop, which lowering writes, uses these.
                BinaryOp::BitAnd => left & right,
                BinaryOp::BitOr => left | right,
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
