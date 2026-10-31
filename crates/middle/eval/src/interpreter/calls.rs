//! Calls and control flow: a call is a `Frame` on a list, so a program's
//! recursion uses no Rust stack.

use std::rc::Rc;

use arena::Id;
use model::DefId;
use model::constant::Value;
use model::layout::in_memory;
use model::layout::layout;
use model::mir;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Terminator;
use model::semantic_types::TypeId;
use source::Span;

use super::CALL_LIMIT;
use super::Failure;
use super::Frame;
use super::Interpreter;
use super::REGISTER_LIMIT;
use super::Register;
use super::Slots;

impl<'b> Interpreter<'b, '_> {
    /// Where the values of `body` go in a frame, worked out once.
    fn slots_of(&mut self, body: &'b mir::Body) -> Rc<Slots> {
        let (types, target) = (self.types, self.target);
        let slots = self.slots.entry(std::ptr::from_ref(body));
        Rc::clone(slots.or_insert_with(|| {
            let mut size = 0u64;
            let mut place = |ty: TypeId| {
                let (bytes, align) = layout(types, ty, target);
                let offset = size.next_multiple_of(align);
                size = offset.saturating_add(bytes);
                offset
            };
            let variables = (body.variables.ids())
                .map(|id| place(body.variables[id].ty))
                .collect();
            let instructions = (body.instructions.ids())
                .map(|id| {
                    let ty = body.instructions[id].ty;
                    if in_memory(types, ty) { place(ty) } else { 0 }
                })
                .collect();
            Rc::new(Slots {
                variables,
                instructions,
                size,
            })
        }))
    }

    /// Starts a call of `function`, whose value goes to `result` of the frame
    /// below, with its memory from `end` on and its arguments the registers
    /// from `registers` on.
    pub(super) fn push_frame(
        &mut self,
        function: DefId,
        body: &'b mir::Body,
        result: Option<Id<mir::Instruction>>,
        end: u64,
        registers: usize,
        span: Span,
    ) -> Result<(), Failure> {
        if self.frames.len() == CALL_LIMIT {
            return Err(self.fail(format!("this went {CALL_LIMIT} calls deep"), span));
        }
        let slots = self.slots_of(body);
        let start = end.next_multiple_of(16); // as far as any type aligns
        let reached = start.saturating_add(slots.size);
        let used = self.registers.len() + body.instructions.len();
        if used > REGISTER_LIMIT || self.memory.grow_stack(reached).is_none() {
            return Err(Failure::Stopped {
                message: "this ran out of stack".to_owned(),
                function: self.frames.last().map_or(function, |frame| frame.function),
                span,
            });
        }
        self.registers.resize(used, None);
        self.frames.push(Frame {
            function,
            body,
            slots,
            start,
            registers,
            block: Id::from_index(0),
            next: 0,
            previous: None,
            result,
        });
        Ok(())
    }

    /// Runs until the first frame returns.
    pub(super) fn run_frames(&mut self) -> Result<Option<Value>, Failure> {
        'frames: loop {
            let frame = self.frame();
            let body = frame.body;
            let block = &body.blocks[frame.block];
            if frame.next == 0 {
                self.enter_block(block)?;
            }
            let done = self.frame().next;
            let instructions = body.lists.items(block.instructions);
            for (index, id) in instructions.enumerate().skip(done) {
                self.step(body.instructions.span(id))?;
                let instruction = &body.instructions[id];
                if let InstructionKind::Call {
                    function,
                    ref arguments,
                } = instruction.kind
                {
                    self.frames.last_mut().expect("a run has a frame").next = index + 1;
                    self.call(id, function, arguments)?;
                    continue 'frames;
                }
                let value = self.run_instruction(id)?;
                self.set_value(id, value)?;
            }
            let span = match block.terminator {
                Terminator::Branch { span, .. }
                | Terminator::Return { span, .. }
                | Terminator::Panic { span, .. }
                | Terminator::FallOff(span) => span,
                Terminator::Goto(_) => body.blocks.span(self.frame().block),
            };
            self.step(span)?;
            let next = match block.terminator {
                Terminator::Goto(target) => target,
                Terminator::Branch {
                    ref condition,
                    then,
                    otherwise,
                    ..
                } => match self.operand(condition) {
                    Register::Scalar(Value::Bool(true)) => then,
                    _ => otherwise,
                },
                Terminator::Return { ref value, .. } => {
                    let value = value.as_ref().map(|value| self.operand(value));
                    if let Some(result) = self.finish_call(value, span)? {
                        return Ok(result);
                    }
                    continue;
                }
                Terminator::Panic { message, span } => {
                    let Value::Bytes(ref text) = body.constants[message.index()].value else {
                        unreachable!("a panic's message is bytes")
                    };
                    return Err(self.fail(String::from_utf8_lossy(text), span));
                }
                Terminator::FallOff(_) => {
                    if let Some(result) = self.finish_call(None, span)? {
                        return Ok(result);
                    }
                    continue;
                }
            };
            let frame = self.frames.last_mut().expect("a run has a frame");
            frame.previous = Some(frame.block);
            frame.block = next;
            frame.next = 0;
        }
    }

    /// Runs the phis `block` starts with, all from the values before them:
    /// one phi may read another's value of the pass before.
    fn enter_block(&mut self, block: &mir::Block) -> Result<(), Failure> {
        let body = self.frame().body;
        let phis = (body.lists.items(block.instructions))
            .take_while(|&id| matches!(body.instructions[id].kind, InstructionKind::Phi(_)));
        let mut values = Vec::new();
        for id in phis {
            self.step(body.instructions.span(id))?;
            let InstructionKind::Phi(ref incoming) = body.instructions[id].kind else {
                unreachable!("only phis are taken")
            };
            let previous = self.frame().previous;
            let (_, value) = incoming
                .iter()
                .find(|&&(from, _)| Some(from) == previous)
                .expect("a phi lists the block control came from");
            values.push((id, self.operand(value)));
        }
        self.frames.last_mut().expect("a run has a frame").next = values.len();
        for (id, value) in values {
            self.set_value(id, Some(value))?;
        }
        Ok(())
    }

    /// Calls `function` for the instruction `id`, after the frame now running.
    fn call(
        &mut self,
        id: Id<mir::Instruction>,
        function: DefId,
        arguments: &[Operand],
    ) -> Result<(), Failure> {
        let frame = self.frame();
        let span = frame.body.instructions.span(id);
        let Some(body) = (self.mir_of)(function) else {
            return Err(Failure::NoMir {
                callee: function,
                function: frame.function,
                span,
            });
        };
        let end = frame.start + frame.slots.size;
        let registers = self.registers.len();
        for argument in arguments {
            let register = self.operand(argument);
            self.registers.push(Some(register));
        }
        self.push_frame(function, body, Some(id), end, registers, span)
    }

    /// Ends the running call with `value`, and gives the run's result if it
    /// was the first.
    fn finish_call(
        &mut self,
        value: Option<Register>,
        span: Span,
    ) -> Result<Option<Option<Value>>, Failure> {
        let result = self.frame().result;
        let Some(result) = result else {
            let ty = self.frame().body.result;
            return Ok(Some(match value {
                Some(value) => Some(self.constant_of(&value, ty, span)?),
                None => None,
            }));
        };
        // A value in memory is still there: it is copied before a frame
        // takes the memory again.
        let frame = self.frames.pop().expect("a run has a frame");
        self.registers.truncate(frame.registers);
        self.set_value(result, value)?;
        Ok(None)
    }
}
