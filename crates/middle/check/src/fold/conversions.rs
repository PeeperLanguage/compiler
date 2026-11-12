//! What `as` gives on constant values.

use std::borrow::Cow;

use model::constant::Value;
use model::mir::Class;
use model::semantic_types::Type;

use eval::operations;
use eval::operations::RegisterValue;
use lower::arithmetic::cast_conversion;

/// `value` of the type `from` as a value of the type `to`, which it converts
/// to (`Types::conversion`), as LLVM converts it: a wider integer keeps the
/// value (a signed one its sign), a narrower one keeps the low bits, `true`
/// is 1, a float rounds to the nearest value of its new type (ties to even),
/// and a float becomes an integer cut toward zero, saturated at the ends of
/// the integer's range, and 0 when NaN (as Rust's `as`).
pub fn convert(from: &Type, to: &Type, value: &Value) -> Value {
    let register = match value {
        Value::Integer(limbs) => {
            let Type::Integer(from) = from else {
                unreachable!("an integer value has an integer type")
            };
            RegisterValue::of_limbs(from.bits, Cow::Borrowed(limbs))
        }
        &Value::Bool(value) => RegisterValue::Narrow(u128::from(value)),
        &Value::Float(bits) => {
            let Type::Float(from) = *from else {
                unreachable!("a float value has a float type")
            };
            RegisterValue::Narrow(operations::float_bits(from.class(), f64::from_bits(bits)))
        }
        // The only constant pointer is `none`, null as every pointer type.
        Value::Null => return Value::Null,
        Value::Bytes(_) => unreachable!("check converts no string"),
    };
    let ((from_class, from_bits), (to_class, to_bits)) = (number(from), number(to));
    let converted = operations::convert(
        cast_conversion(from, to),
        from_class,
        from_bits,
        to_class,
        to_bits,
        &register,
    );
    match to {
        Type::Float(_) => {
            Value::Float(operations::float_value(to_class, converted.narrow()).to_bits())
        }
        _ => Value::Integer(converted.limbs(to_bits).into_owned()),
    }
}

/// The class and width of a value of the type `ty`, a number or a `bool`;
/// an integer at its own width.
fn number(ty: &Type) -> (Class, u32) {
    match *ty {
        Type::Integer(integer) => (Class::integer(integer.bits), integer.bits),
        Type::Float(float) => (float.class(), float.class().bits()),
        Type::Bool => (Class::I8, 1),
        _ => unreachable!("check converts {ty:?} only to and from a raw pointer"),
    }
}
