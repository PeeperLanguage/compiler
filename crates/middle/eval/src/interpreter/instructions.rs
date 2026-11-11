//! What each instruction does, but a call, which changes what runs next
//! (`run_frames`). An integer of up to 128 bits is worked on in a `u128`
//! cut to its class, a wider one with `bigint`; a float as its own
//! precision, as LLVM's operations do.

use arena::Id;
use bigint::Limbs;
use model::core_mir::Class;
use model::core_mir::Comparison;
use model::core_mir::Conversion;
use model::core_mir::FloatOp;
use model::core_mir::Instruction;
use model::core_mir::InstructionKind;
use model::core_mir::IntegerOp;
use model::core_mir::Operand;
use model::core_mir::Place;
use model::core_mir::Root;
use model::core_mir::Step;
use runtime_abi::messages::DIVISION_BY_ZERO;
use runtime_abi::messages::shift_count_message;

use super::ADDRESS_NOT_KNOWN;
use super::Failure;
use super::INVALID_ACCESS;
use super::Interpreter;

impl Interpreter<'_, '_> {
    /// Runs the instruction `id`, giving its value, if it has one.
    pub(super) fn run_instruction(&mut self, id: Id<Instruction>) -> Result<Option<u128>, Failure> {
        let body = self.frame().body;
        let at = self.first_value + id.index();
        let value = match &body.instructions[id].kind {
            &InstructionKind::Integer {
                operation,
                class,
                left,
                right,
            } => {
                // Only `rawptr + usize` works on the pointer class.
                if class == Class::Pointer && self.host.is_none() {
                    let message = "a pointer offset cannot run at compile time, \
                        as it could read an address";
                    return Err(self.fail(message, self.span_of(id)));
                }
                if let Class::Wide(bits) = class {
                    let (left, right) = (self.wide(left), self.wide(right));
                    let value = wide_integer(operation, bits, left, right);
                    let value = value.map_err(|message| self.fail(message, self.span_of(id)))?;
                    self.of_limbs(at, class, value)
                } else {
                    let bits = self.class_bits(class);
                    let (left, right) = (self.operand(left), self.operand(right));
                    let value = integer(operation, bits, left, right);
                    value.map_err(|message| self.fail(message, self.span_of(id)))?
                }
            }
            &InstructionKind::Float {
                operation,
                class,
                left,
                right,
            } => float(operation, class, self.operand(left), self.operand(right)),
            &InstructionKind::Compare {
                comparison,
                class,
                left,
                right,
            } => {
                let compared = if let Class::Wide(bits) = class {
                    let (left, right) = (self.wide(left), self.wide(right));
                    wide_compare(comparison, bits, left, right)
                } else {
                    let (left, right) = (self.operand(left), self.operand(right));
                    if class == Class::Pointer && self.host.is_none() && left != 0 && right != 0 {
                        return Err(self.fail(ADDRESS_NOT_KNOWN, self.span_of(id)));
                    }
                    compare(comparison, class, self.class_bits(class), left, right)
                };
                u128::from(compared)
            }
            &InstructionKind::Convert {
                conversion,
                from,
                to,
                value,
            } => {
                // `none` stays `none`; any other address could reach an
                // address's bytes.
                if conversion == Conversion::PointerCast
                    && self.host.is_none()
                    && self.operand(value) != 0
                {
                    let message = "a pointer cast cannot run at compile time, \
                        as it could read an address";
                    return Err(self.fail(message, self.span_of(id)));
                }
                self.convert(at, conversion, from, to, value)
            }
            InstructionKind::Load { class, place } => {
                let address = self.address(place, id)?;
                if let Class::Wide(bits) = *class {
                    let size = class.bytes(self.pointer_bytes());
                    let value = Limbs::from_le_bytes(bits, self.read_bytes(address, size, id)?);
                    self.of_limbs(at, *class, value)
                } else {
                    self.load(*class, address, id)?
                }
            }
            InstructionKind::Store {
                class,
                place,
                value,
            } => {
                let address = self.address(place, id)?;
                let value = self.operand(*value);
                self.store(*class, address, value, id)?;
                return Ok(None);
            }
            InstructionKind::Copy { to, from, size } => {
                let (to, from) = (self.address(to, id)?, self.address(from, id)?);
                self.count_copy(*size, id)?;
                let copied = self.memory.copy(from, to, *size);
                copied.ok_or_else(|| self.fail(INVALID_ACCESS, self.span_of(id)))?;
                return Ok(None);
            }
            InstructionKind::Address(place) | InstructionKind::Borrow { place, .. } => {
                u128::from(self.address(place, id)?)
            }
            InstructionKind::Call { .. } => unreachable!("`run_frames` runs a call"),
            InstructionKind::Check { failed, message } => {
                if self.operand(*failed) != 0 {
                    return Err(self.fail(&**message, self.span_of(id)));
                }
                return Ok(None);
            }
            InstructionKind::Moved(_) => return Ok(None),
            InstructionKind::Dead(slot) => {
                if let Some(started) = self.frame_mut().slots.get_mut(slot.index()) {
                    let started = std::mem::take(started);
                    if started != 0 {
                        self.memory.close_block(started);
                    }
                }
                return Ok(None);
            }
        };
        Ok(Some(value))
    }

    /// The integer `operand` of the class `Wide`.
    fn wide(&self, operand: Operand) -> &Limbs {
        &self.wides[self.operand(operand) as usize]
    }

    /// `value`, of the class `from`, as the class `to`, for the register at
    /// `at`.
    fn convert(
        &mut self,
        at: usize,
        conversion: Conversion,
        from: Class,
        to: Class,
        value: Operand,
    ) -> u128 {
        let (from_bits, to_bits) = (self.class_bits(from), self.class_bits(to));
        let register = self.operand(value);
        // A wide integer on either side goes through limbs.
        let limbs = match from {
            Class::Wide(_) => self.wides[register as usize].clone(),
            _ => Limbs::new(from_bits, &[register as u64, (register >> 64) as u64]),
        };
        let is_wide = matches!(from, Class::Wide(_)) || matches!(to, Class::Wide(_));
        match conversion {
            Conversion::SignExtend | Conversion::ZeroExtend if is_wide => {
                let signed = conversion == Conversion::SignExtend;
                let extended = bigint::extend(from_bits, signed, &limbs, to_bits);
                self.of_limbs(at, to, extended)
            }
            Conversion::Truncate if is_wide => {
                self.of_limbs(at, to, Limbs::new(to_bits, limbs.as_slice()))
            }
            Conversion::SignedToFloat | Conversion::UnsignedToFloat if is_wide => {
                let signed = conversion == Conversion::SignedToFloat;
                float_bits(to, bigint::to_float(from_bits, signed, &limbs, to_bits))
            }
            // Saturated at the integer's own width, which may be below its
            // class's.
            Conversion::FloatToSigned { bits } | Conversion::FloatToUnsigned { bits } => {
                let signed = matches!(conversion, Conversion::FloatToSigned { .. });
                let integer = bigint::from_float(bits, signed, float_value(from, register));
                self.of_limbs(at, to, bigint::extend(bits, signed, &integer, to_bits))
            }
            _ => convert(conversion, from, from_bits, to, to_bits, register),
        }
    }

    /// The address of `place`: its root, then each step, reading the
    /// pointer at each `Through`. A sum wraps at the pointer's width, as
    /// any number a raw pointer holds may be wrong: the access that follows
    /// is what refuses it.
    pub(super) fn address(&mut self, place: &Place, id: Id<Instruction>) -> Result<u64, Failure> {
        let mut address = match place.root {
            Root::Slot(slot) => self.slot_address(slot, id)?,
            Root::At(operand) => self.operand(operand) as u64,
        };
        let wrap = mask(self.pointer_bits) as u64;
        for &step in &place.steps {
            address = match step {
                Step::Offset(bytes) => address.wrapping_add(bytes) & wrap,
                Step::Through => self.read_word(address, id)?,
                Step::Index { index, size } => {
                    let offset = (self.operand(index) as u64).wrapping_mul(size);
                    address.wrapping_add(offset) & wrap
                }
            };
        }
        Ok(address)
    }

    /// The `length` bytes at `address`, or the run stops at the instruction `id`.
    pub(super) fn read_bytes(
        &self,
        address: u64,
        length: u64,
        id: Id<Instruction>,
    ) -> Result<&[u8], Failure> {
        let bytes = self.memory.bytes(address, length);
        bytes.ok_or_else(|| self.fail(INVALID_ACCESS, self.span_of(id)))
    }

    /// The pointer kept at `address`.
    fn read_word(&self, address: u64, id: Id<Instruction>) -> Result<u64, Failure> {
        Ok(self.load(Class::Pointer, address, id)? as u64)
    }

    /// The register of `class`, not `Wide`, kept at `address`.
    fn load(&self, class: Class, address: u64, id: Id<Instruction>) -> Result<u128, Failure> {
        let size = class.bytes(self.pointer_bytes());
        let bytes = self.read_bytes(address, size, id)?;
        let mut word = [0; 16];
        word[..size as usize].copy_from_slice(bytes);
        Ok(u128::from_le_bytes(word))
    }

    /// Keeps `value`, a register of `class`, at `address`.
    fn store(
        &mut self,
        class: Class,
        address: u64,
        value: u128,
        id: Id<Instruction>,
    ) -> Result<(), Failure> {
        let size = class.bytes(self.pointer_bytes());
        let Some(bytes) = self.memory.bytes_mut(address, size) else {
            return Err(self.fail(INVALID_ACCESS, self.span_of(id)));
        };
        match class {
            Class::Wide(_) => self.wides[value as usize].write_le_bytes(bytes),
            _ => bytes.copy_from_slice(&value.to_le_bytes()[..size as usize]),
        }
        Ok(())
    }
}

/// The bits of a `u128` an integer of `bits` bits uses.
pub(super) fn mask(bits: u32) -> u128 {
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
/// pointer wrote are not checked, so those trap here, as the machine does.
fn integer(operation: IntegerOp, bits: u32, left: u128, right: u128) -> Result<u128, String> {
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
fn float_value(class: Class, bits: u128) -> f64 {
    match class {
        Class::F32 => f64::from(f32::from_bits(bits as u32)),
        _ => f64::from_bits(bits as u64),
    }
}

/// The bits of `value` as a float of `class`, rounded to it.
fn float_bits(class: Class, value: f64) -> u128 {
    match class {
        Class::F32 => u128::from((value as f32).to_bits()),
        _ => u128::from(value.to_bits()),
    }
}

/// `left operation right` on floats of `class`, each in its own
/// precision. Which NaN one gives is not specified, as in LLVM.
fn float(operation: FloatOp, class: Class, left: u128, right: u128) -> u128 {
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
fn compare(comparison: Comparison, class: Class, bits: u32, left: u128, right: u128) -> bool {
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
    use std::cmp::Ordering;
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
/// bits; neither is wide. A float goes to an integer toward zero, to the
/// nearest end of its range when outside it, and NaN to 0, as Rust's `as`
/// does at each width.
fn convert(
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
            unreachable!("a float becomes an integer through limbs")
        }
        Conversion::FloatWiden | Conversion::FloatNarrow => float_bits(to, float()),
    };
    converted & mask(to_bits)
}
