//! Registers, and moving values between them and memory, in the bytes the
//! target keeps them in.

use arena::Id;
use bigint::Limbs;
use model::constant::Value;
use model::layout::in_memory;
use model::mir;
use model::mir::Operand;
use model::semantic_types::Float;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use source::Span;

use super::Failure;
use super::INVALID_ACCESS;
use super::Interpreter;
use super::Register;

impl<'b> Interpreter<'b, '_> {
    /// What the instruction `id` gave. A value `in_memory` gets its own copy,
    /// as what it was read from may change before it is used.
    pub(super) fn set_value(
        &mut self,
        id: Id<mir::Instruction>,
        value: Option<Register>,
    ) -> Result<(), Failure> {
        let body = self.frame().body;
        let ty = body.instructions[id].ty;
        let value = match value {
            Some(Register::Address(from)) if in_memory(self.types, ty) => {
                let to = self.value_slot(id);
                if from != to {
                    let (span, size) = (body.instructions.span(id), self.size_of(ty));
                    self.charge(size, span)?;
                    let copied = self.memory.copy(from, to, size);
                    copied.ok_or_else(|| self.fail(INVALID_ACCESS, span))?;
                }
                Some(Register::Address(to))
            }
            value => value,
        };
        let frame = self.frame();
        let slot = frame.registers + frame.body.parameters + id.index();
        self.registers[slot] = value;
        Ok(())
    }

    /// Where the value `id`, kept in memory, is built.
    pub(super) fn value_slot(&self, id: Id<mir::Instruction>) -> u64 {
        let frame = self.frame();
        frame.start + frame.slots.instructions[id.index()]
    }

    pub(super) fn operand(&mut self, operand: &Operand) -> Register {
        let frame = self.frame();
        match *operand {
            Operand::Constant(constant) => {
                let body = frame.body;
                self.register_of(&body.constants[constant.index()].value)
            }
            Operand::Argument(index) => self.registers[frame.registers + index as usize]
                .clone()
                .expect("a call has its arguments"),
            Operand::Value(value) => self.registers
                [frame.registers + frame.body.parameters + value.index()]
            .clone()
            .expect("an instruction gives its value before it is used"),
        }
    }

    /// The register of a constant.
    pub(super) fn register_of(&mut self, value: &Value) -> Register {
        match value {
            Value::Bytes(text) => Register::Address(self.memory.string(text)),
            Value::Null => Register::Address(0),
            value => Register::Scalar(value.clone()),
        }
    }

    /// The constant `value`, of the type `ty`, is, for the end of a run.
    pub(super) fn constant_of(
        &self,
        value: &Register,
        ty: TypeId,
        span: Span,
    ) -> Result<Value, Failure> {
        match (value, &self.types[ty]) {
            (Register::Scalar(value), _) => Ok(value.clone()),
            (&Register::Address(0), Type::RawPointer { .. }) => Ok(Value::Null),
            (&Register::Address(address), Type::CString) => match self.memory.string_at(address) {
                Some(text) => Ok(Value::Bytes(text)),
                None => Err(self.fail(INVALID_ACCESS, span)),
            },
            _ => Err(self.fail("a constant cannot hold this value yet", span)),
        }
    }

    /// The `length` bytes at `address`, or the run stops at `span`.
    pub(super) fn read_bytes(
        &self,
        address: u64,
        length: u64,
        span: Span,
    ) -> Result<&[u8], Failure> {
        (self.memory.bytes(address, length)).ok_or_else(|| self.fail(INVALID_ACCESS, span))
    }

    /// The address kept at `address`.
    pub(super) fn read_address(&self, address: u64, span: Span) -> Result<u64, Failure> {
        let mut bytes = [0; 8];
        let size = self.pointer_bytes();
        bytes[..size as usize].copy_from_slice(self.read_bytes(address, size, span)?);
        Ok(u64::from_le_bytes(bytes))
    }

    /// The value of type `ty` at `address`.
    pub(super) fn read_value(
        &self,
        address: u64,
        ty: TypeId,
        span: Span,
    ) -> Result<Register, Failure> {
        let scalar = match self.types[ty] {
            Type::Bool => Value::Bool(self.read_bytes(address, 1, span)?[0] != 0),
            Type::Integer(integer) => {
                let bytes = self.read_bytes(address, self.size_of(ty), span)?;
                Value::Integer(Limbs::from_le_bytes(integer.bits, bytes))
            }
            Type::Float(Float::F32) => {
                let bytes = self.read_bytes(address, 4, span)?;
                let bits = u32::from_le_bytes(bytes.try_into().expect("four bytes were read"));
                Value::Float(f64::from(f32::from_bits(bits)).to_bits())
            }
            Type::Float(Float::F64) => {
                let bytes = self.read_bytes(address, 8, span)?;
                Value::Float(u64::from_le_bytes(
                    bytes.try_into().expect("eight bytes were read"),
                ))
            }
            Type::CString | Type::Reference { .. } | Type::RawPointer { .. } => {
                return Ok(Register::Address(self.read_address(address, span)?));
            }
            Type::Slice { .. } => {
                return Ok(Register::Slice {
                    address: self.read_address(address, span)?,
                    length: self.read_address(address + self.pointer_bytes(), span)?,
                });
            }
            Type::Struct { .. } | Type::Array { .. } | Type::Range { .. } => {
                return Ok(Register::Address(address));
            }
            _ => unreachable!("no value is kept with this type"),
        };
        Ok(Register::Scalar(scalar))
    }

    /// Writes `bytes` at `address`.
    fn write_bytes(&mut self, address: u64, bytes: &[u8], span: Span) -> Result<(), Failure> {
        match self.memory.bytes_mut(address, bytes.len() as u64) {
            Some(to) => {
                to.copy_from_slice(bytes);
                Ok(())
            }
            None => Err(self.fail(INVALID_ACCESS, span)),
        }
    }

    /// Writes `value`, of type `ty`, at `address`.
    pub(super) fn write_value(
        &mut self,
        address: u64,
        ty: TypeId,
        value: &Register,
        span: Span,
    ) -> Result<(), Failure> {
        let pointer = self.pointer_bytes() as usize;
        match *value {
            Register::Scalar(Value::Bool(value)) => {
                self.write_bytes(address, &[u8::from(value)], span)
            }
            Register::Scalar(Value::Integer(ref limbs)) => {
                let size = self.size_of(ty);
                match self.memory.bytes_mut(address, size) {
                    Some(bytes) => {
                        limbs.write_le_bytes(bytes);
                        Ok(())
                    }
                    None => Err(self.fail(INVALID_ACCESS, span)),
                }
            }
            Register::Scalar(Value::Float(bits)) => match self.types[ty] {
                Type::Float(Float::F32) => {
                    let narrow = f64::from_bits(bits) as f32;
                    self.write_bytes(address, &narrow.to_le_bytes(), span)
                }
                _ => self.write_bytes(address, &bits.to_le_bytes(), span),
            },
            Register::Scalar(_) => unreachable!("strings and `none` are addresses here"),
            Register::Address(from) if in_memory(self.types, ty) => {
                let size = self.size_of(ty);
                self.charge(size, span)?;
                let copied = self.memory.copy(from, address, size);
                copied.ok_or_else(|| self.fail(INVALID_ACCESS, span))
            }
            Register::Address(value) => {
                self.write_bytes(address, &value.to_le_bytes()[..pointer], span)
            }
            Register::Slice {
                address: elements,
                length,
            } => {
                self.write_bytes(address, &elements.to_le_bytes()[..pointer], span)?;
                let second = address + pointer as u64;
                self.write_bytes(second, &length.to_le_bytes()[..pointer], span)
            }
        }
    }
}
