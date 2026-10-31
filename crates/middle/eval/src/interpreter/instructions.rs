//! What each instruction does, but for a call or a phi, which change what
//! runs next (`calls`).

use arena::Id;
use model::constant::Value;
use model::mir;
use model::mir::InstructionKind;
use model::operators::BinaryOp;

use super::Failure;
use super::Interpreter;
use super::Register;
use crate::conversions;
use crate::messages;
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
        let value = match body.instructions[id].kind {
            InstructionKind::Load(ref place) | InstructionKind::Move(ref place) => {
                let located = self.locate(place, span)?;
                let ty = body.instructions[id].ty;
                Some(self.read_value(located.address, ty, span)?)
            }
            InstructionKind::Store {
                ref place,
                ref value,
            } => {
                let located = self.locate(place, span)?;
                let register = self.operand(value);
                let ty = body.operand_type(*value);
                self.write_value(located.address, ty, &register, span)?;
                None
            }
            InstructionKind::Binary {
                op,
                ref left,
                ref right,
            } => {
                let left_type = &types[body.operand_type(*left)];
                let right_type = &types[body.operand_type(*right)];
                match (self.operand(left), self.operand(right)) {
                    (Register::Scalar(left), Register::Scalar(right)) => {
                        let value = operators::binary(op, left_type, &left, right_type, &right)
                            .map_err(|trap| {
                                self.fail(messages::trap_message(trap, left_type), span)
                            })?;
                        Some(Register::Scalar(value))
                    }
                    (Register::Address(left), Register::Address(right)) => {
                        let equal = left == right;
                        let value = equal == (op == BinaryOp::EqualTo);
                        Some(Register::Scalar(Value::Bool(value)))
                    }
                    _ => unreachable!("check gives `{op:?}` two numbers or two pointers"),
                }
            }
            InstructionKind::Unary { op, ref value } => {
                let ty = &types[body.operand_type(*value)];
                let Register::Scalar(value) = self.operand(value) else {
                    unreachable!("a unary operator takes a number or a `bool`")
                };
                let value = operators::unary(op, ty, &value);
                Some(Register::Scalar(
                    value.expect("unary operators do not trap"),
                ))
            }
            InstructionKind::Convert(ref value) => {
                let from = &types[body.operand_type(*value)];
                let to = &types[body.instructions[id].ty];
                Some(match self.operand(value) {
                    Register::Scalar(value) => {
                        Register::Scalar(conversions::convert(from, to, &value))
                    }
                    pointer => pointer, // a pointer cast keeps the address
                })
            }
            InstructionKind::Call { .. } | InstructionKind::Phi(_) => {
                unreachable!("`run_frames` and `enter_block` run these")
            }
            InstructionKind::Check {
                ref failed,
                message,
            } => {
                if let Register::Scalar(Value::Bool(true)) = self.operand(failed) {
                    return Err(self.fail(message, span));
                }
                None
            }
            InstructionKind::ScopeEnd(_) => None,
            InstructionKind::Borrow { ref place, .. } | InstructionKind::Address(ref place) => {
                Some(self.borrow(id, place)?)
            }
            InstructionKind::Aggregate(ref parts) => Some(self.aggregate(id, parts, span)?),
            InstructionKind::ArrayParts(ref parts) => Some(self.array_parts(id, parts, span)?),
            InstructionKind::Field {
                ref value,
                ref fields,
            } => Some(self.field(id, value, fields)?),
            InstructionKind::Print {
                newline,
                ref values,
            } => {
                if self.host.is_none() {
                    let message = "`print` calls cannot run at compile time yet";
                    return Err(self.fail(message, span));
                }
                self.print(newline, values, span)?;
                None
            }
        };
        Ok(value)
    }
}
