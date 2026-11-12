//! What each instruction does, but a call, which changes what runs next
//! (`run_frames`). The operations themselves are `crate::operations`.

use std::borrow::Cow;

use arena::Id;
use bigint::Limbs;
use model::mir::Class;
use model::mir::Conversion;
use model::mir::Instruction;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Place;
use model::mir::Root;
use model::mir::Step;

use crate::operations;
use crate::operations::RegisterValue;

use super::ADDRESS_NOT_KNOWN;
use super::Halt;
use super::INVALID_ACCESS;
use super::Interpreter;

impl Interpreter<'_, '_> {
    /// Runs the instruction `id`, giving its value, if it has one.
    pub(super) fn run_instruction(&mut self, id: Id<Instruction>) -> Result<Option<u128>, Halt> {
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
                if class == Class::Pointer && self.memory.is_constant() {
                    let message = "a pointer offset cannot run at compile time, \
                        as it could read an address";
                    return Err(self.fail(message, self.span_of(id)));
                }
                let (left, right) = (self.register(class, left), self.register(class, right));
                let value = operations::integer(operation, self.class_bits(class), &left, &right);
                let value = value.map_err(|message| self.fail(message, self.span_of(id)))?;
                self.hold(at, class, value)
            }
            &InstructionKind::Float {
                operation,
                class,
                left,
                right,
            } => operations::float(operation, class, self.operand(left), self.operand(right)),
            &InstructionKind::Compare {
                comparison,
                class,
                left,
                right,
            } => {
                if class == Class::Pointer
                    && self.memory.is_constant()
                    && self.operand(left) != 0
                    && self.operand(right) != 0
                {
                    return Err(self.fail(ADDRESS_NOT_KNOWN, self.span_of(id)));
                }
                let (left, right) = (self.register(class, left), self.register(class, right));
                let bits = self.class_bits(class);
                u128::from(operations::compare(comparison, class, bits, &left, &right))
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
                    && self.memory.is_constant()
                    && self.operand(value) != 0
                {
                    let message = "a pointer cast cannot run at compile time, \
                        as it could read an address";
                    return Err(self.fail(message, self.span_of(id)));
                }
                let (from_bits, to_bits) = (self.class_bits(from), self.class_bits(to));
                let value = self.register(from, value);
                let value = operations::convert(conversion, from, from_bits, to, to_bits, &value);
                self.hold(at, to, value)
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
            InstructionKind::Address { place, .. } | InstructionKind::Borrow { place, .. } => {
                u128::from(self.address(place, id)?)
            }
            InstructionKind::Call { .. } => unreachable!("`run_frames` runs a call"),
            InstructionKind::Check { failed, message } => {
                if self.operand(*failed) != 0 {
                    return Err(self.fail(&**message, self.span_of(id)));
                }
                return Ok(None);
            }
            InstructionKind::Moved { .. } => return Ok(None),
            InstructionKind::Dead(slot) => {
                // A program's frame keeps no slot starts: its slots stay put.
                let started = self.frame_mut().slots.get_mut(slot.index());
                if let Some(started) = started.and_then(Option::take) {
                    self.memory.close_block(started.get());
                }
                return Ok(None);
            }
        };
        Ok(Some(value))
    }

    /// What `operand`, of `class`, holds.
    fn register(&self, class: Class, operand: Operand) -> RegisterValue<'_> {
        match class {
            Class::Wide(_) => {
                RegisterValue::Wide(Cow::Borrowed(&self.wides[self.operand(operand) as usize]))
            }
            _ => RegisterValue::Narrow(self.operand(operand)),
        }
    }

    /// The register at `at`, of `class`, holding `value`.
    fn hold(&mut self, at: usize, class: Class, value: RegisterValue) -> u128 {
        match value {
            RegisterValue::Narrow(value) => value,
            RegisterValue::Wide(limbs) => self.of_limbs(at, class, limbs.into_owned()),
        }
    }

    /// The address of `place`: its root, then each step, reading the
    /// pointer at each `Through`. A sum wraps at the pointer's width, as
    /// any number a raw pointer holds may be wrong: the access that follows
    /// is what refuses it.
    pub(super) fn address(&mut self, place: &Place, id: Id<Instruction>) -> Result<u64, Halt> {
        let mut address = match place.root {
            Root::Slot(slot) => self.slot_address(slot, id)?,
            Root::At(operand) => self.operand(operand) as u64,
            Root::Result => self.registers[self.first_register] as u64,
        };
        let wrap = operations::mask(self.pointer_bits) as u64;
        for &step in &place.steps {
            address = match step {
                Step::Offset(bytes) => address.wrapping_add(bytes) & wrap,
                Step::Through => self.read_word(address, id)?,
                Step::Index { index, size, .. } => {
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
    ) -> Result<&[u8], Halt> {
        let bytes = self.memory.bytes(address, length);
        bytes.ok_or_else(|| self.fail(INVALID_ACCESS, self.span_of(id)))
    }

    /// The pointer kept at `address`.
    fn read_word(&self, address: u64, id: Id<Instruction>) -> Result<u64, Halt> {
        Ok(self.load(Class::Pointer, address, id)? as u64)
    }

    /// The register of `class`, not `Wide`, kept at `address`.
    fn load(&self, class: Class, address: u64, id: Id<Instruction>) -> Result<u128, Halt> {
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
    ) -> Result<(), Halt> {
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
