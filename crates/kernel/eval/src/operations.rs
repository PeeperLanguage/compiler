//! What each MIR operation gives on the values registers hold: the one
//! meaning of Peeper's arithmetic, comparisons and conversions. The
//! interpreter runs instructions with it (`interpreter`), and check folds
//! constant operators with it (`check::fold`), at the type's own width;
//! LLVM must compute the same (check's tests/llvm_parity.rs).
//!
//! An integer of up to 128 bits is worked on in a `u128` cut to `bits`, a
//! wider one with `bigint`; a float in its own precision, as LLVM's
//! operations do.

use std::borrow::Cow;
use std::cmp::Ordering;

use bigint::Limbs;
use model::mir::Class;
use model::mir::Comparison;
use model::mir::Conversion;
use model::mir::FloatOp;
use model::mir::IntegerOp;
use runtime_abi::messages::DIVISION_BY_ZERO;
use runtime_abi::messages::shift_count_message;

/// What a register holds: up to 128 bits (an integer, a float's bits or an
/// address), or the limbs of an integer wider than that.
pub enum RegisterValue<'l> {
    Narrow(u128),
    Wide(Cow<'l, Limbs>),
}

impl<'l> RegisterValue<'l> {
    /// The register of `limbs`, an integer of `bits` bits.
    pub fn of_limbs(bits: u32, limbs: Cow<'l, Limbs>) -> RegisterValue<'l> {
        if bits > 128 {
            return RegisterValue::Wide(limbs);
        }
        let limbs = limbs.as_slice();
        RegisterValue::Narrow(u128::from(limbs[0]) | u128::from(limbs[1]) << 64)
    }

    /// The integer of `bits` bits it holds, in limbs.
    pub fn limbs(&self, bits: u32) -> Cow<'_, Limbs> {
        match self {
            RegisterValue::Narrow(value) => {
                Cow::Owned(Limbs::new(bits, &[*value as u64, (*value >> 64) as u64]))
            }
            RegisterValue::Wide(limbs) => Cow::Borrowed(limbs),
        }
    }

    /// The value of up to 128 bits it holds.
    pub fn narrow(&self) -> u128 {
        match self {
            RegisterValue::Narrow(value) => *value,
            RegisterValue::Wide(_) => unreachable!("a wide value is an integer past 128 bits"),
        }
    }
}

/// `left operation right` on integers of `bits` bits, wrapping there; the
/// lowest signed value over -1 wraps to itself. `Err` when it traps: a
/// divisor of zero, or a shift count not below `bits`.
pub fn integer(
    operation: IntegerOp,
    bits: u32,
    left: &RegisterValue,
    right: &RegisterValue,
) -> Result<RegisterValue<'static>, String> {
    match (left, right) {
        (RegisterValue::Wide(left), RegisterValue::Wide(right)) => {
            wide_integer(operation, bits, left, right)
                .map(|limbs| RegisterValue::of_limbs(bits, Cow::Owned(limbs)))
        }
        _ => narrow_integer(operation, bits, left.narrow(), right.narrow())
            .map(RegisterValue::Narrow),
    }
}

/// Whether `left` and `right`, of `class` and `bits` bits, compare as
/// `comparison` says.
pub fn compare(
    comparison: Comparison,
    class: Class,
    bits: u32,
    left: &RegisterValue,
    right: &RegisterValue,
) -> bool {
    match (left, right) {
        (RegisterValue::Wide(left), RegisterValue::Wide(right)) => {
            wide_compare(comparison, bits, left, right)
        }
        _ => narrow_compare(comparison, class, bits, left.narrow(), right.narrow()),
    }
}

/// `value`, of `from` of `from_bits` bits, as `to` of `to_bits` bits. A
/// float goes to an integer toward zero, to the nearest end of its range
/// when outside it, and NaN to 0, as Rust's `as` does at each width; it is
/// saturated at the integer's own width, which may be below its class's.
pub fn convert(
    conversion: Conversion,
    from: Class,
    from_bits: u32,
    to: Class,
    to_bits: u32,
    value: &RegisterValue,
) -> RegisterValue<'static> {
    if let Conversion::FloatToSigned { bits } | Conversion::FloatToUnsigned { bits } = conversion {
        let signed = matches!(conversion, Conversion::FloatToSigned { .. });
        let integer = bigint::from_float(bits, signed, float_value(from, value.narrow()));
        return RegisterValue::of_limbs(
            to_bits,
            Cow::Owned(bigint::extend(bits, signed, &integer, to_bits)),
        );
    }
    if from_bits <= 128 && to_bits <= 128 {
        let converted = narrow_convert(conversion, from, from_bits, to, to_bits, value.narrow());
        return RegisterValue::Narrow(converted);
    }
    // A wide integer on either side goes through limbs.
    let limbs = value.limbs(from_bits);
    match conversion {
        Conversion::SignExtend | Conversion::ZeroExtend => {
            let signed = conversion == Conversion::SignExtend;
            RegisterValue::of_limbs(
                to_bits,
                Cow::Owned(bigint::extend(from_bits, signed, &limbs, to_bits)),
            )
        }
        Conversion::Truncate => {
            RegisterValue::of_limbs(to_bits, Cow::Owned(Limbs::new(to_bits, limbs.as_slice())))
        }
        Conversion::SignedToFloat | Conversion::UnsignedToFloat => {
            let signed = conversion == Conversion::SignedToFloat;
            RegisterValue::Narrow(float_bits(
                to,
                bigint::to_float(from_bits, signed, &limbs, to_bits),
            ))
        }
        _ => unreachable!("only integers are wider than 128 bits"),
    }
}

/// The bits of a `u128` an integer of `bits` bits uses.
pub(crate) fn mask(bits: u32) -> u128 {
    match bits {
        128.. => u128::MAX,
        _ => (1 << bits) - 1,
    }
}

/// `value`, of `bits` bits, read as signed.
fn signed(bits: u32, value: u128) -> i128 {
    let unused = 128 - bits;
    ((value << unused) as i128) >> unused
}

/// `left operation right` on integers of `bits` bits, wrapping there; the
/// lowest signed value over -1 wraps to itself. Lowering checks a divisor
/// against zero and a shift count against the width, but bytes a raw
/// pointer wrote are not checked, so those trap here, though a machine's
/// shift would not.
fn narrow_integer(
    operation: IntegerOp,
    bits: u32,
    left: u128,
    right: u128,
) -> Result<u128, String> {
    let nonzero = || {
        if right == 0 {
            return Err(DIVISION_BY_ZERO.to_owned());
        }
        Ok(right)
    };
    let count = || {
        if right >= u128::from(bits) {
            return Err(shift_count_message(bits));
        }
        Ok(right as u32)
    };
    let value = match operation {
        IntegerOp::Add => left.wrapping_add(right),
        IntegerOp::Subtract => left.wrapping_sub(right),
        IntegerOp::Multiply => left.wrapping_mul(right),
        IntegerOp::SignedDivide => {
            nonzero()?;
            signed(bits, left).wrapping_div(signed(bits, right)) as u128
        }
        IntegerOp::UnsignedDivide => left / nonzero()?,
        IntegerOp::SignedRemainder => {
            nonzero()?;
            signed(bits, left).wrapping_rem(signed(bits, right)) as u128
        }
        IntegerOp::UnsignedRemainder => left % nonzero()?,
        IntegerOp::ShiftLeft => left << count()?,
        IntegerOp::SignedShiftRight => (signed(bits, left) >> count()?) as u128,
        IntegerOp::UnsignedShiftRight => left >> count()?,
        IntegerOp::And => left & right,
        IntegerOp::Or => left | right,
        IntegerOp::Xor => left ^ right,
    };
    Ok(value & mask(bits))
}

/// `left operation right` on integers of `bits` bits, more than 128, which
/// trap as `integer`'s do.
fn wide_integer(
    operation: IntegerOp,
    bits: u32,
    left: &Limbs,
    right: &Limbs,
) -> Result<Limbs, String> {
    let count = || {
        let count = bigint::to_u64(bits, false, right).filter(|&count| count < u64::from(bits));
        count
            .map(|count| count as u32)
            .ok_or_else(|| shift_count_message(bits))
    };
    let divided = |quotient: Option<Limbs>| quotient.ok_or_else(|| DIVISION_BY_ZERO.to_owned());
    Ok(match operation {
        IntegerOp::Add => bigint::add(bits, left, right),
        IntegerOp::Subtract => bigint::subtract(bits, left, right),
        IntegerOp::Multiply => bigint::multiply(bits, left, right),
        IntegerOp::SignedDivide => divided(bigint::divide(bits, true, left, right))?,
        IntegerOp::UnsignedDivide => divided(bigint::divide(bits, false, left, right))?,
        IntegerOp::SignedRemainder => divided(bigint::remainder(bits, true, left, right))?,
        IntegerOp::UnsignedRemainder => divided(bigint::remainder(bits, false, left, right))?,
        IntegerOp::ShiftLeft => bigint::shift_left(bits, left, count()?),
        IntegerOp::SignedShiftRight => bigint::shift_right(bits, true, left, count()?),
        IntegerOp::UnsignedShiftRight => bigint::shift_right(bits, false, left, count()?),
        IntegerOp::And => bigint::bit_and(bits, left, right),
        IntegerOp::Or => bigint::bit_or(bits, left, right),
        IntegerOp::Xor => bigint::bit_xor(bits, left, right),
    })
}

/// The float of `class` whose bits a register holds, as an `f64`, which
/// holds an `f32` exactly.
pub fn float_value(class: Class, bits: u128) -> f64 {
    match class {
        Class::F32 => f64::from(f32::from_bits(bits as u32)),
        _ => f64::from_bits(bits as u64),
    }
}

/// The bits of `value` as a float of `class`, rounded to it.
pub fn float_bits(class: Class, value: f64) -> u128 {
    match class {
        Class::F32 => u128::from((value as f32).to_bits()),
        _ => u128::from(value.to_bits()),
    }
}

/// `left operation right` on floats of `class`, each in its own
/// precision. Which NaN one gives is not specified, as in LLVM.
pub fn float(operation: FloatOp, class: Class, left: u128, right: u128) -> u128 {
    fn apply<
        F: std::ops::Add<Output = F>
            + std::ops::Sub<Output = F>
            + std::ops::Mul<Output = F>
            + std::ops::Div<Output = F>
            + std::ops::Rem<Output = F>,
    >(
        operation: FloatOp,
        left: F,
        right: F,
    ) -> F {
        match operation {
            FloatOp::Add => left + right,
            FloatOp::Subtract => left - right,
            FloatOp::Multiply => left * right,
            FloatOp::Divide => left / right,
            FloatOp::Remainder => left % right,
        }
    }
    if class == Class::F32 {
        let (left, right) = (f32::from_bits(left as u32), f32::from_bits(right as u32));
        return u128::from(apply(operation, left, right).to_bits());
    }
    let (left, right) = (f64::from_bits(left as u64), f64::from_bits(right as u64));
    u128::from(apply(operation, left, right).to_bits())
}

/// Whether `left` and `right`, registers of `class` of `bits` bits,
/// compare as `comparison` says. A float's `Equal` and `NotEqual` are a
/// float's: NaN equals nothing, and `-0.0` equals `0.0`.
fn narrow_compare(
    comparison: Comparison,
    class: Class,
    bits: u32,
    left: u128,
    right: u128,
) -> bool {
    if matches!(class, Class::F32 | Class::F64) {
        let (left, right) = (float_value(class, left), float_value(class, right));
        return match comparison {
            Comparison::Equal => left == right,
            Comparison::NotEqual => left != right,
            Comparison::FloatLess => left < right,
            Comparison::FloatLessOrEqual => left <= right,
            _ => unreachable!("floats compare as floats"),
        };
    }
    match comparison {
        Comparison::Equal => left == right,
        Comparison::NotEqual => left != right,
        Comparison::SignedLess => signed(bits, left) < signed(bits, right),
        Comparison::SignedLessOrEqual => signed(bits, left) <= signed(bits, right),
        Comparison::UnsignedLess => left < right,
        Comparison::UnsignedLessOrEqual => left <= right,
        Comparison::FloatLess | Comparison::FloatLessOrEqual => {
            unreachable!("only floats compare as floats")
        }
    }
}

/// Whether `left` and `right`, integers of `bits` bits, more than 128,
/// compare as `comparison` says.
fn wide_compare(comparison: Comparison, bits: u32, left: &Limbs, right: &Limbs) -> bool {
    let order = |signed| bigint::compare(bits, signed, left, right);
    match comparison {
        Comparison::Equal => left == right,
        Comparison::NotEqual => left != right,
        Comparison::SignedLess => order(true) == Ordering::Less,
        Comparison::SignedLessOrEqual => order(true) != Ordering::Greater,
        Comparison::UnsignedLess => order(false) == Ordering::Less,
        Comparison::UnsignedLessOrEqual => order(false) != Ordering::Greater,
        Comparison::FloatLess | Comparison::FloatLessOrEqual => {
            unreachable!("only floats compare as floats")
        }
    }
}

/// `value`, a register of `from` of `from_bits` bits, as `to` of `to_bits`
/// bits; neither is wide, and it is no float becoming an integer.
fn narrow_convert(
    conversion: Conversion,
    from: Class,
    from_bits: u32,
    to: Class,
    to_bits: u32,
    value: u128,
) -> u128 {
    let float = || float_value(from, value);
    let converted = match conversion {
        Conversion::SignExtend => signed(from_bits, value) as u128,
        Conversion::ZeroExtend | Conversion::Truncate | Conversion::PointerCast => value,
        Conversion::SignedToFloat => {
            let value = signed(from_bits, value);
            match to {
                Class::F32 => u128::from((value as f32).to_bits()),
                _ => u128::from((value as f64).to_bits()),
            }
        }
        Conversion::UnsignedToFloat => match to {
            Class::F32 => u128::from((value as f32).to_bits()),
            _ => u128::from((value as f64).to_bits()),
        },
        Conversion::FloatToSigned { .. } | Conversion::FloatToUnsigned { .. } => {
            unreachable!("`convert` turns a float into an integer through limbs")
        }
        Conversion::FloatWiden | Conversion::FloatNarrow => float_bits(to, float()),
    };
    converted & mask(to_bits)
}
