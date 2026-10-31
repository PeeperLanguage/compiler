use bigint::Limbs;

use model::constant::Value;
use model::operators::BinaryOp;
use model::operators::UnaryOp;
use model::semantic_types::Float;
use model::semantic_types::Integer;
use model::semantic_types::Type;

use crate::conversions::convert;
use crate::operators::ValueError;
use crate::operators::binary;
use crate::operators::known_trap;
use crate::operators::unary;

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
fn bit_operators_on_bools_as_a_range_loop_uses_them() {
    let bits = |op, left, right| {
        binary(
            op,
            &Type::Bool,
            &Value::Bool(left),
            &Type::Bool,
            &Value::Bool(right),
        )
    };
    assert_eq!(bits(BinaryOp::BitOr, false, true), Ok(Value::Bool(true)));
    assert_eq!(bits(BinaryOp::BitOr, false, false), Ok(Value::Bool(false)));
    assert_eq!(bits(BinaryOp::BitAnd, true, true), Ok(Value::Bool(true)));
    assert_eq!(bits(BinaryOp::BitAnd, true, false), Ok(Value::Bool(false)));
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
    let fold = |op, ty: &Type, left, right| binary(op, ty, &float(ty, left), ty, &float(ty, right));
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

/// A trap known from the right operand alone, whatever the left one is.
#[test]
fn known_traps() {
    let (i32, u8) = (int(true, 32), int(false, 8));
    let trap =
        |op, left: &Type, right: &Type, count| known_trap(op, left, right, &value(right, count));
    assert_eq!(
        trap(BinaryOp::Divide, &i32, &i32, 0),
        Some(ValueError::DivisionByZero)
    );
    assert_eq!(
        trap(BinaryOp::Remainder, &i32, &i32, 0),
        Some(ValueError::DivisionByZero)
    );
    assert_eq!(trap(BinaryOp::Divide, &i32, &i32, -1), None);
    assert_eq!(
        trap(BinaryOp::ShiftLeft, &i32, &u8, 32),
        Some(ValueError::ShiftOutOfRange)
    );
    assert_eq!(trap(BinaryOp::ShiftRight, &i32, &u8, 31), None);
    assert_eq!(trap(BinaryOp::Add, &i32, &i32, 0), None);
    let f64 = Type::Float(Float::F64);
    let zero = Value::Float(0f64.to_bits());
    assert_eq!(known_trap(BinaryOp::Divide, &f64, &f64, &zero), None);
}
