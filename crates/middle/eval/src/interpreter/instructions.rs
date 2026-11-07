//! What each instruction does, but for a call or a phi, which change what
//! runs next (`calls`).

use arena::Id;
use bigint::Limbs;
use model::constant::Value;
use model::mir;
use model::mir::InstructionKind;
use model::operators::BinaryOp;
use model::semantic_types::Type;

use super::ADDRESS_NOT_KNOWN;
use super::Failure;
use super::Interpreter;
use super::Register;
use crate::conversions;
use crate::operators;

impl<'b> Interpreter<'b, '_> {
    /// Runs the instruction `id`, not a call or a phi, giving its value, if
    /// it has one.
    pub(super) fn run_instruction(
        &mut self,
        id: Id<mir::Instruction>,
    ) -> Result<Option<Register>, Failure> {
        let body = self.frame().body;
        let span = body.instructions.span(id);
        let types = self.types;
        let value = match &body.instructions[id].kind {
            InstructionKind::Load(place) | InstructionKind::Move(place) => {
                let located = self.locate(place, span)?;
                let ty = body.instructions[id].ty;
                Some(self.read_value(located.address, ty, span)?)
            }
            // After the elements' address.
            InstructionKind::Length(place) => {
                let located = self.locate(place, span)?;
                let length = located.address.wrapping_add(self.pointer_bytes());
                let ty = body.instructions[id].ty;
                Some(self.read_value(length, ty, span)?)
            }
            InstructionKind::Store { place, value } => {
                let located = self.locate(place, span)?;
                let register = self.operand(value);
                let ty = body.operand_type(*value);
                self.write_value(located.address, ty, &register, span)?;
                None
            }
            InstructionKind::Binary { op, left, right } => {
                let left_type = &types[body.operand_type(*left)];
                let right_type = &types[body.operand_type(*right)];
                match (self.operand(left), self.operand(right)) {
                    (Register::Scalar(left), Register::Scalar(right)) => {
                        let value = operators::binary(*op, left_type, &left, right_type, &right)
                            .map_err(|message| self.fail(message, span))?;
                        Some(Register::Scalar(value))
                    }
                    // `rawptr + usize`; a wrong address stops the run where it
                    // is used.
                    (Register::Address(address), Register::Scalar(Value::Integer(offset))) => {
                        if self.host.is_none() {
                            let message = "a pointer offset cannot run at compile time, \
                                as it could read an address";
                            return Err(self.fail(message, span));
                        }
                        let bits = self.target.pointer_bits;
                        let sum = bigint::add(bits, &Limbs::new(bits, &[address]), &offset);
                        let sum = bigint::to_u64(bits, false, &sum)
                            .expect("a sum wrapped to a pointer's bits fits 64 bits");
                        Some(Register::Address(sum))
                    }
                    (Register::Address(left), Register::Address(right)) => {
                        if self.host.is_none() && left != 0 && right != 0 {
                            return Err(self.fail(ADDRESS_NOT_KNOWN, span));
                        }
                        let equal = left == right;
                        let value = equal == (*op == BinaryOp::EqualTo);
                        Some(Register::Scalar(Value::Bool(value)))
                    }
                    _ => unreachable!(
                        "check gives `{op:?}` two numbers, two pointers, or a pointer and an offset"
                    ),
                }
            }
            InstructionKind::Unary { op, value } => {
                let ty = &types[body.operand_type(*value)];
                let Register::Scalar(value) = self.operand(value) else {
                    unreachable!("a unary operator takes a number or a `bool`")
                };
                Some(Register::Scalar(operators::unary(*op, ty, &value)))
            }
            InstructionKind::Convert(value) => {
                let from = &types[body.operand_type(*value)];
                let to = &types[body.instructions[id].ty];
                Some(match self.operand(value) {
                    Register::Scalar(value) => {
                        Register::Scalar(conversions::convert(from, to, &value))
                    }
                    Register::Address(0) => Register::Address(0), // `none` stays `none`
                    // A cast keeps the address, but it is the way to a
                    // pointer's bytes, and to what is next to a value.
                    _ if self.host.is_none() && matches!(to, Type::RawPointer { .. }) => {
                        let message = "a pointer cast cannot run at compile time, \
                            as it could read an address";
                        return Err(self.fail(message, span));
                    }
                    pointer => pointer,
                })
            }
            InstructionKind::Call { .. } | InstructionKind::Phi(_) => {
                unreachable!("`run_frames` and `enter_block` run these")
            }
            InstructionKind::Check { failed, message } => {
                if let Register::Scalar(Value::Bool(true)) = self.operand(failed) {
                    return Err(self.fail(&**message, span));
                }
                None
            }
            InstructionKind::ScopeEnd(variable) => {
                let frame = self.frames.last_mut().expect("a run has a frame");
                let started = frame.variables.get_mut(variable.index());
                if let Some(address) = started.filter(|address| **address != 0) {
                    self.memory.close_block(std::mem::take(address));
                }
                None
            }
            InstructionKind::Borrow { place, .. } | InstructionKind::Address(place) => {
                Some(self.borrow(id, place)?)
            }
            InstructionKind::ToInterface {
                reference,
                implementation,
            } => {
                let Register::Address(address) = self.operand(reference) else {
                    unreachable!("a reference's register is an address")
                };
                let table = implementation.index() as u64;
                Some(Register::Interface { address, table })
            }
            InstructionKind::Aggregate(parts) => Some(self.aggregate(id, parts, span)?),
            InstructionKind::ArrayParts(parts) => Some(self.array_parts(id, parts, span)?),
            InstructionKind::Field { value, fields } => Some(self.field(id, value, fields)?),
            InstructionKind::Print { newline, values } => {
                if self.host.is_none() {
                    let message = "`print` calls cannot run at compile time yet";
                    return Err(self.fail(message, span));
                }
                self.print(*newline, values, span)?;
                None
            }
        };
        Ok(value)
    }
}
