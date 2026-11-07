//! Values made of parts: building structs, arrays and ranges, reading a field
//! of a value, and counting and filling a range.

use arena::Id;
use bigint::Limbs;
use model::constant::Value;
use model::layout::field_placed;
use model::mir;
use model::mir::Operand;
use model::operators::BinaryOp;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::range_field;
use source::Span;

use super::Failure;
use super::Interpreter;
use super::Register;
use crate::operators;
use runtime_abi::messages;

impl<'b> Interpreter<'b, '_> {
    /// Builds the struct, array or range `id` from `parts`.
    pub(super) fn aggregate(
        &mut self,
        id: Id<mir::Instruction>,
        parts: &[Operand],
        span: Span,
    ) -> Result<Register, Failure> {
        let body = self.frame().body;
        let ty = body.instructions[id].ty;
        let slot = self.value_slot(id);
        for (index, part) in parts.iter().enumerate() {
            let offset = match self.types[ty] {
                Type::Array { element, .. } => index as u64 * self.size_of(element),
                _ => field_placed(self.types, ty, index, self.target).0,
            };
            let value = self.operand(part);
            self.write_value(slot + offset, body.operand_type(*part), &value, span)?;
        }
        Ok(Register::Address(slot))
    }

    /// The field `fields` lead to in `value`, not in a variable; a slice's
    /// length is its field 1.
    pub(super) fn field(
        &mut self,
        id: Id<mir::Instruction>,
        value: &Operand,
        fields: &[u32],
    ) -> Result<Register, Failure> {
        let body = self.frame().body;
        let span = body.instructions.span(id);
        let ty = body.instructions[id].ty;
        let mut owner = body.operand_type(*value);
        match self.operand(value) {
            Register::Slice { address, .. } if fields == [0] => Ok(Register::Address(address)),
            Register::Slice { length, .. } => {
                let Type::Integer(integer) = self.types[ty] else {
                    unreachable!("a slice's length is a `usize`")
                };
                let length = Limbs::new(integer.bits, &[length]);
                Ok(Register::Scalar(Value::Integer(length)))
            }
            Register::Address(mut address) => {
                for &index in fields {
                    let (offset, field) =
                        field_placed(self.types, owner, index as usize, self.target);
                    address += offset;
                    owner = field;
                }
                self.read_value(address, ty, span)
            }
            Register::Scalar(_) | Register::Interface { .. } => {
                unreachable!("a number or an interface reference has no fields")
            }
        }
    }

    /// A range value's field `field`.
    fn range_part(
        &mut self,
        range: u64,
        ty: TypeId,
        field: usize,
        span: Span,
    ) -> Result<Value, Failure> {
        let (offset, field_type) = field_placed(self.types, ty, field, self.target);
        match self.read_value(range + offset, field_type, span)? {
            Register::Scalar(value) => Ok(value),
            _ => unreachable!("a range's fields are numbers and a `bool`"),
        }
    }

    /// How many values the range at `range` gives, as `Stmt::ForRange` counts
    /// them: `None` past every array's length. Lowering stops a zero step when
    /// a range is made, but a raw pointer can give any bytes as a range.
    fn range_count(&mut self, range: u64, ty: TypeId, span: Span) -> Result<Option<u64>, Failure> {
        let Type::Range { step, .. } = self.types[ty] else {
            unreachable!("only a range has a count")
        };
        let Type::Integer(integer) = self.types[step] else {
            unreachable!("a step is an integer")
        };
        let left = self.range_part(range, ty, range_field::LEFT, span)?;
        let by = self.range_part(range, ty, range_field::STEP, span)?;
        let inclusive = self.range_part(range, ty, range_field::INCLUSIVE, span)?;
        let (Value::Integer(left), Value::Integer(by)) = (left, by) else {
            unreachable!("a range's left and step are integers")
        };
        let count = integer.count_over(&left, &by, inclusive == Value::Bool(true));
        let count = count.ok_or_else(|| self.fail(messages::DIVISION_BY_ZERO, span))?;
        Ok(bigint::to_u64(integer.bits + 1, false, &count))
    }

    /// Builds the array `id` from `parts` (`InstructionKind::ArrayParts`),
    /// after checking that they give its length, as the backend's
    /// `write_part_counts` does.
    pub(super) fn array_parts(
        &mut self,
        id: Id<mir::Instruction>,
        parts: &[Operand],
        span: Span,
    ) -> Result<Register, Failure> {
        let body = self.frame().body;
        let ty = body.instructions[id].ty;
        let Type::Array {
            element, length, ..
        } = self.types[ty]
        else {
            unreachable!("array parts make an array")
        };
        let mut values = Vec::with_capacity(parts.len());
        let mut total = Some(0u64);
        for part in parts {
            let part_type = body.operand_type(*part);
            let value = self.operand(part);
            let count = match (&self.types[part_type], &value) {
                _ if self.types.same(part_type, element) => Some(1),
                (&Type::Array { length, .. }, _) => Some(length),
                (_, &Register::Slice { length, .. }) => Some(length),
                (Type::Range { .. }, &Register::Address(range)) => {
                    self.range_count(range, part_type, span)?
                }
                _ => unreachable!("the checker spreads only an array, a slice or a range"),
            };
            total = total
                .zip(count)
                .and_then(|(sum, count)| sum.checked_add(count));
            values.push((*part, value, count.unwrap_or(0)));
        }
        if total != Some(length) {
            return Err(self.fail(messages::array_parts_message(length), span));
        }
        let slot = self.value_slot(id);
        let size = self.size_of(element);
        let mut at = slot;
        for (part, value, count) in values {
            let part_type = body.operand_type(part);
            match value {
                value if self.types.same(part_type, element) => {
                    self.write_value(at, element, &value, span)?;
                }
                Register::Address(range) if matches!(self.types[part_type], Type::Range { .. }) => {
                    self.fill_range(at, range, part_type, element, count, span)?;
                }
                Register::Address(from) | Register::Slice { address: from, .. } => {
                    self.copy_memory(from, at, count * size, span)?;
                }
                Register::Scalar(_) | Register::Interface { .. } => {
                    unreachable!("a spread part is in memory")
                }
            }
            at += count * size;
        }
        Ok(Register::Address(slot))
    }

    /// Writes the `count` values of the range at `range` from `to` on: its
    /// start, then each `delta` more than the one before, wrapping.
    fn fill_range(
        &mut self,
        to: u64,
        range: u64,
        ty: TypeId,
        element: TypeId,
        count: u64,
        span: Span,
    ) -> Result<(), Failure> {
        self.count_steps(u32::try_from(count).unwrap_or(u32::MAX), span)?;
        let delta = self.range_part(range, ty, range_field::DELTA, span)?;
        let mut value = self.range_part(range, ty, range_field::START, span)?;
        let size = self.size_of(element);
        for index in 0..count {
            let register = Register::Scalar(value);
            self.write_value(to + index * size, element, &register, span)?;
            let Register::Scalar(written) = register else {
                unreachable!("made a scalar above")
            };
            let element_type = &self.types[element];
            value = operators::binary(BinaryOp::Add, element_type, &written, element_type, &delta)
                .expect("adding does not trap");
        }
        Ok(())
    }
}
