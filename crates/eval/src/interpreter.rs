//! Running a function's MIR at compile time, on constant values: what a
//! constant whose value needs a call is. Each operation means what `binary`,
//! `unary` and `convert` say, as when check folds, so a function gives the
//! same result here as when the program runs.

use arena::Id;
use model::DefId;
use model::constant::Value;
use model::mir;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Terminator;
use model::semantic_types::Types;
use source::Span;

/// How many instructions and terminators one evaluation may run, so an
/// endless loop stops with an error instead of hanging the compiler or an
/// editor.
const STEP_LIMIT: u32 = 1_000_000;

/// Why a function could not run at compile time, at `span` in the body of
/// `function`.
#[derive(Debug)]
pub enum Failure {
    /// It stopped with `message`.
    Stopped {
        message: String,
        function: DefId,
        span: Span,
    },
    /// It called `callee`, for which `mir_of` gave no MIR: the caller of
    /// `run` knows why.
    NoMir {
        callee: DefId,
        function: DefId,
        span: Span,
    },
}

/// What `function`, whose MIR is `body`, returns for `arguments`: `None` for
/// a function without a result. A function it calls has the MIR `mir_of`
/// gives, if any.
pub fn run<'b>(
    function: DefId,
    body: &'b mir::Body,
    arguments: &[Value],
    types: &'b Types,
    mir_of: &dyn Fn(DefId) -> Option<&'b mir::Body>,
) -> Result<Option<Value>, Failure> {
    let mut machine = Machine {
        types,
        mir_of,
        steps: 0,
    };
    machine.call(function, body, arguments)
}

struct Machine<'b, 'f> {
    types: &'b Types,
    mir_of: &'f dyn Fn(DefId) -> Option<&'b mir::Body>,
    steps: u32, // run so far, by every call
}

/// One running call: its function, MIR and arguments, the value of each of
/// its variables and instructions so far, and the block control came from.
struct Frame<'b, 'a> {
    function: DefId,
    body: &'b mir::Body,
    arguments: &'a [Value],
    variables: Vec<Option<Value>>,
    values: Vec<Option<Value>>,
    previous: Option<Id<mir::Block>>,
}

impl Frame<'_, '_> {
    fn operand(&self, operand: &Operand) -> Value {
        match *operand {
            Operand::Constant(constant) => self.body.constants[constant.index()].value.clone(),
            Operand::Argument(index) => self.arguments[index as usize].clone(),
            Operand::Value(value) => self.values[value.index()]
                .clone()
                .expect("an instruction gives its value before it is used"),
        }
    }

    /// The call stopped at `span` with `message`.
    fn fail(&self, message: String, span: Span) -> Failure {
        Failure::Stopped {
            message,
            function: self.function,
            span,
        }
    }
}

impl<'b> Machine<'b, '_> {
    /// Runs one call of `function`, whose MIR is `body`, to its `return`.
    fn call(
        &mut self,
        function: DefId,
        body: &'b mir::Body,
        arguments: &[Value],
    ) -> Result<Option<Value>, Failure> {
        // A deep recursion in the program is a deep one here.
        const RED_ZONE: usize = 128 * 1024;
        const SEGMENT: usize = 1024 * 1024;
        stacker::maybe_grow(RED_ZONE, SEGMENT, || {
            let mut frame = Frame {
                function,
                body,
                arguments,
                variables: vec![None; body.variables.len()],
                values: vec![None; body.instructions.len()],
                previous: None,
            };
            let mut block = Id::from_index(0);
            loop {
                let mir::Block {
                    instructions,
                    ref terminator,
                    ..
                } = body.blocks[block];
                for id in body.lists.items(instructions) {
                    self.step(&frame, body.instructions.span(id))?;
                    frame.values[id.index()] = self.run_instruction(&mut frame, id)?;
                }
                let span = match *terminator {
                    Terminator::Branch { span, .. }
                    | Terminator::Return { span, .. }
                    | Terminator::Panic { span, .. }
                    | Terminator::FallOff(span) => span,
                    Terminator::Goto(_) => body.blocks.span(block),
                };
                self.step(&frame, span)?;
                frame.previous = Some(block);
                block = match *terminator {
                    Terminator::Goto(target) => target,
                    Terminator::Branch {
                        ref condition,
                        then,
                        otherwise,
                        ..
                    } => match frame.operand(condition) {
                        Value::Bool(true) => then,
                        _ => otherwise,
                    },
                    Terminator::Return { ref value, .. } => {
                        return Ok(value.as_ref().map(|value| frame.operand(value)));
                    }
                    Terminator::Panic { message, span } => {
                        let Value::Bytes(ref text) = body.constants[message.index()].value else {
                            unreachable!("a panic's message is bytes")
                        };
                        return Err(frame.fail(String::from_utf8_lossy(text).into_owned(), span));
                    }
                    Terminator::FallOff(_) => return Ok(None),
                };
            }
        })
    }

    /// Runs instruction `id` of `frame`, giving its value, if it has one.
    fn run_instruction(
        &mut self,
        frame: &mut Frame<'b, '_>,
        id: Id<mir::Instruction>,
    ) -> Result<Option<Value>, Failure> {
        let body = frame.body;
        let span = body.instructions.span(id);
        let unsupported = |frame: &Frame, what: &str| {
            let message = format!("{what} cannot run at compile time yet");
            Err(frame.fail(message, span))
        };
        let types = self.types;
        let value = match body.instructions[id].kind {
            InstructionKind::Load(ref place) | InstructionKind::Move(ref place) => {
                let Some(variable) = place.whole_variable() else {
                    return unsupported(frame, "fields, elements and dereferences");
                };
                let value = frame.variables[variable.index()].clone();
                Some(value.expect("flow lets only a set variable be read"))
            }
            InstructionKind::Store {
                ref place,
                ref value,
            } => {
                let Some(variable) = place.whole_variable() else {
                    return unsupported(frame, "fields, elements and dereferences");
                };
                frame.variables[variable.index()] = Some(frame.operand(value));
                None
            }
            InstructionKind::Binary {
                op,
                ref left,
                ref right,
            } => {
                let left_type = &types[body.operand_type(*left)];
                let right_type = &types[body.operand_type(*right)];
                let (left, right) = (frame.operand(left), frame.operand(right));
                let value = crate::binary(op, left_type, &left, right_type, &right)
                    .map_err(|trap| frame.fail(crate::trap_message(trap, left_type), span))?;
                Some(value)
            }
            InstructionKind::Unary { op, ref value } => {
                let ty = &types[body.operand_type(*value)];
                let value = crate::unary(op, ty, &frame.operand(value));
                Some(value.expect("unary operators do not trap"))
            }
            InstructionKind::Convert(ref value) => {
                let from = &types[body.operand_type(*value)];
                let to = &types[body.instructions[id].ty];
                Some(crate::convert(from, to, &frame.operand(value)))
            }
            InstructionKind::Call {
                function: callee,
                ref arguments,
            } => {
                let Some(callee_body) = (self.mir_of)(callee) else {
                    return Err(Failure::NoMir {
                        callee,
                        function: frame.function,
                        span,
                    });
                };
                let arguments: Vec<_> = arguments
                    .iter()
                    .map(|argument| frame.operand(argument))
                    .collect();
                self.call(callee, callee_body, &arguments)?
            }
            InstructionKind::Phi(ref incoming) => {
                let (_, value) = incoming
                    .iter()
                    .find(|&&(from, _)| Some(from) == frame.previous)
                    .expect("a phi lists the block control came from");
                Some(frame.operand(value))
            }
            InstructionKind::Check {
                ref failed,
                message,
            } => {
                if frame.operand(failed) == Value::Bool(true) {
                    return Err(frame.fail(message.to_string(), span));
                }
                None
            }
            InstructionKind::ScopeEnd(_) => None,
            InstructionKind::Borrow { .. } | InstructionKind::Address(_) => {
                return unsupported(frame, "references, slices and addresses");
            }
            InstructionKind::Aggregate(_)
            | InstructionKind::ArrayParts(_)
            | InstructionKind::Field { .. } => {
                return unsupported(frame, "struct and array values");
            }
            InstructionKind::Print { .. } => return unsupported(frame, "`print` calls"),
        };
        Ok(value)
    }

    /// Counts one more step of `frame`, run at `span`: an error past
    /// `STEP_LIMIT`.
    fn step(&mut self, frame: &Frame, span: Span) -> Result<(), Failure> {
        self.steps += 1;
        if self.steps > STEP_LIMIT {
            let message = format!("this ran more than {STEP_LIMIT} steps");
            return Err(frame.fail(message, span));
        }
        Ok(())
    }
}
