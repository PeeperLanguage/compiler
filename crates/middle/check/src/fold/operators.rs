//! What each operator gives on constant values.
//!
//! Integers have any width from 1 to 2^23 bits. Arithmetic wraps to the width,
//! as the language's fixtures expect (`127i8 + 1i8 == -128i8`); division rounds
//! toward zero and `MIN / -1` is `MIN`, as the runtime defines it. Division by
//! zero and out-of-range shifts trap at run time, so they have no value here.
//!
//! Floats follow IEEE 754 in their own precision, as LLVM's `fadd`, `fsub`,
//! `fmul`, `fdiv` and `frem` do: an `f32` operation computes in `f32`.
//! Division by zero gives an infinity. `==` is true only for ordered equal
//! values and `!=` is its negation (`fcmp oeq` and `une`); `<` and the others
//! are false when either side is NaN. Which NaN an operation gives is not
//! specified, as in LLVM.

use std::borrow::Cow;

use bigint::Limbs;
use model::constant::Value;
use model::mir::Class;
use model::mir::IntegerOp;
use model::operators::BinaryOp;
use model::operators::UnaryOp;
use model::semantic_types::Integer;
use model::semantic_types::Type;

use eval::operations;
use eval::operations::RegisterValue;
use lower::arithmetic::BinaryOperation;
use lower::arithmetic::binary_operation;

/// `left op right`. Both operands have one type, except that a shift count
/// has its own integer type (`value << count`). Check only gives an operator
/// operands it accepts. `Err` is what a program that traps here is told.
pub fn binary(
    op: BinaryOp,
    left_type: &Type,
    left: &Value,
    right_type: &Type,
    right: &Value,
) -> Result<Value, String> {
    debug_assert!(
        matches!(op, BinaryOp::ShiftLeft | BinaryOp::ShiftRight) || left_type == right_type,
        "check gives `{op:?}` operands of one type"
    );
    let count;
    let (class, bits, left, right) = match (left_type, left, right_type, right) {
        (
            &Type::Integer(integer),
            Value::Integer(left),
            &Type::Integer(right_type),
            Value::Integer(right),
        ) => {
            let right = if matches!(op, BinaryOp::ShiftLeft | BinaryOp::ShiftRight) {
                count = shift_count(integer, right_type, right);
                &count
            } else {
                right
            };
            let register = |limbs| RegisterValue::of_limbs(integer.bits, Cow::Borrowed(limbs));
            let class = Class::integer(integer.bits);
            (class, integer.bits, register(left), register(right))
        }
        (&Type::Float(float), &Value::Float(left), _, &Value::Float(right)) => {
            let class = float.class();
            let register =
                |bits| RegisterValue::Narrow(operations::float_bits(class, f64::from_bits(bits)));
            (class, class.bits(), register(left), register(right))
        }
        (Type::Bool, Value::Bool(left), Type::Bool, Value::Bool(right)) => {
            return Ok(Value::Bool(match op {
                BinaryOp::LogicalAnd => *left && *right,
                BinaryOp::LogicalOr => *left || *right,
                BinaryOp::EqualTo => left == right,
                BinaryOp::NotEqualTo => left != right,
                _ => unreachable!("check gives `{op:?}` no bool operands"),
            }));
        }
        // The only constant pointer is `none`.
        (Type::RawPointer { .. }, Value::Null, _, Value::Null) => {
            return Ok(Value::Bool(op == BinaryOp::EqualTo));
        }
        _ => {
            unreachable!("check gives `{op:?}` operands of types {left_type:?} and {right_type:?}")
        }
    };
    Ok(match binary_operation(op, left_type) {
        BinaryOperation::Integer(operation) => {
            integer_value(bits, &operations::integer(operation, bits, &left, &right)?)
        }
        BinaryOperation::Float(operation) => {
            let value = operations::float(operation, class, left.narrow(), right.narrow());
            Value::Float(operations::float_value(class, value).to_bits())
        }
        BinaryOperation::Compare {
            comparison,
            swapped,
        } => {
            let (left, right) = if swapped {
                (right, left)
            } else {
                (left, right)
            };
            Value::Bool(operations::compare(comparison, class, bits, &left, &right))
        }
    })
}

/// `op value`, where the operand has the type `operand_type`; what
/// lowering's instructions for it compute, except that a NaN's sign is
/// flipped too (a float's `Negate`).
pub fn unary(op: UnaryOp, operand_type: &Type, value: &Value) -> Value {
    match (op, operand_type, value) {
        (UnaryOp::LogicalNot, Type::Bool, Value::Bool(value)) => Value::Bool(!value),
        (UnaryOp::Negate, &Type::Integer(integer), Value::Integer(value)) => {
            let zero = Limbs::new(integer.bits, &[]);
            integer_unary(IntegerOp::Subtract, integer, &zero, value)
        }
        // The sign flipped, a NaN's too, as LLVM's `fneg`: `-NAN` is
        // another value than `NAN`. Computing `-0.0 - x` would leave a
        // NaN's sign to the host's arithmetic. A constant `f32` is kept in
        // an `f64`, whose sign is its sign.
        (UnaryOp::Negate, Type::Float(_), &Value::Float(bits)) => Value::Float(bits ^ (1 << 63)),
        (UnaryOp::BitNot, &Type::Integer(integer), Value::Integer(value)) => {
            let ones = vec![u64::MAX; integer.bits.div_ceil(64) as usize];
            integer_unary(
                IntegerOp::Xor,
                integer,
                value,
                &Limbs::new(integer.bits, &ones),
            )
        }
        _ => unreachable!("check gives `{op:?}` an operand of type {operand_type:?}"),
    }
}

/// `left operation right` on integers of the type `integer`, which cannot
/// trap.
fn integer_unary(operation: IntegerOp, integer: Integer, left: &Limbs, right: &Limbs) -> Value {
    let register = |limbs| RegisterValue::of_limbs(integer.bits, Cow::Borrowed(limbs));
    let value = operations::integer(operation, integer.bits, &register(left), &register(right));
    integer_value(
        integer.bits,
        &value.expect("only a division or a shift traps"),
    )
}

/// The integer of `bits` bits `register` holds.
fn integer_value(bits: u32, register: &RegisterValue) -> Value {
    Value::Integer(register.limbs(bits).into_owned())
}

/// The count of a shift of a `shifted` value by `count`, of the type
/// `count_type`, at the shifted type's width: as lowering checks it, a
/// negative count or one past the width is the width, out of range.
fn shift_count(shifted: Integer, count_type: Integer, count: &Limbs) -> Limbs {
    let width = u64::from(shifted.bits);
    let count = bigint::to_u64(count_type.bits, count_type.signed, count)
        .map_or(width, |count| count.min(width));
    Limbs::new(shifted.bits, &[count])
}

/// What a program is told when `op` traps whatever its left operand, of the type `left_type`, is,
/// with the right operand `right`, of the type `right_type`: a division by
/// zero or a shift count out of range. Both depend on the right operand
/// only, so `op` is tried on a zero left one.
pub fn known_trap(
    op: BinaryOp,
    left_type: &Type,
    right_type: &Type,
    right: &Value,
) -> Option<String> {
    let Type::Integer(integer) = left_type else {
        return None; // float division gives an infinity or NaN
    };
    // Skipping the others keeps folding cheap.
    if !op.can_trap() {
        return None;
    }
    let zero = Value::Integer(Limbs::new(integer.bits, &[]));
    binary(op, left_type, &zero, right_type, right).err()
}
