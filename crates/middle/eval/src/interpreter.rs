//! Running a function's MIR (`model::mir`) with no backend: a
//! constant's call (`run_function`), or a whole program (`program::run`).
//! The run knows registers and bytes, never a type: only a constant's
//! arguments and result are read through their shapes (`ConstantShape`),
//! which the caller gives, where they cross into it. Each operation means what LLVM's does, as the program compiled
//! would compute it.
//!
//! Memory (`memory::Memory`) is bytes laid out as the MIR says, so
//! references, slices and raw pointers are real addresses; a constant's run
//! gives each slot, each time it starts, a new block, closed at its `Dead`,
//! so an address of a slot that ended reaches nothing. A call is a `Frame`
//! on a list, so a program's recursion uses no Rust stack and the run ends
//! with an error before any limit of the host.

mod instructions;
mod memory;
pub mod program;

use std::borrow::Cow;
use std::num::NonZeroU64;
use std::rc::Rc;

use arena::Id;
use bigint::Limbs;
use model::DefId;
use model::FunctionCopy;
use model::constant::Value;
use model::machine::Machine;
use model::mir::Block;
use model::mir::Body;
use model::mir::Callee;
use model::mir::Class;
use model::mir::Global;
use model::mir::Instruction;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Passing;
use model::mir::Place;
use model::mir::Signature;
use model::mir::Slot;
use model::mir::Terminator;
use model::mir::constant_value;
use rustc_hash::FxHashMap;
use source::Span;

use crate::operations;
use crate::operations::RegisterValue;
use memory::Memory;
use program::Host;

/// How many instructions and terminators a constant's call may run, so an
/// endless loop stops with an error instead of hanging the compiler or an
/// editor. A copy counts a step for each `BYTES_PER_STEP` bytes too.
const CONSTANT_STEP_LIMIT: u32 = 1_000_000;

/// How many steps a program may run: more than any page should wait for, so a
/// page's own time limit usually ends a program first.
const PROGRAM_STEP_LIMIT: u32 = 2_000_000_000;

/// How many bytes a copy moves for each step it counts as: about what one
/// step costs, as copying is far faster than running an instruction.
const BYTES_PER_STEP: u64 = 256;

/// How many calls may be running at once. A recursion that never ends meets
/// this or `REGISTER_LIMIT` long before a step limit, as each call is a few
/// steps.
const CALL_LIMIT: usize = 100_000;

/// How many arguments and values all running calls may hold, so a large
/// function that recurses cheaply stops before the host runs out of memory.
const REGISTER_LIMIT: usize = 1 << 20;

const INVALID_ACCESS: &str = "this reads or writes memory the program does not own";

/// Why a constant's run stops where it would read an address, not only use one.
pub const ADDRESS_NOT_KNOWN: &str = "an address is known only when the program runs";

const OUT_OF_STACK: &str = "this ran out of stack";

/// How a constant of a type sits in a register or in memory: what a run
/// needs to know of an argument's or a result's type.
#[derive(Clone, Copy, Debug)]
pub enum ConstantShape {
    Bool,
    Integer {
        bits: u32,
        signed: bool,
    },
    Float(Class),
    /// A raw pointer, whose only constant is `none`.
    RawPointer,
    CString,
    Str,
    /// No constant has this type, for the reason given.
    Refused(&'static str),
}

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
    /// It called `callee`, a function or one of its copies, for which
    /// `mir_of` gave no MIR: the caller of `run_function` knows why.
    NoMir {
        callee: FunctionCopy,
        function: DefId,
        span: Span,
    },
}

/// Why a run left its calls before the first returned.
enum Halt {
    Failed(Failure),
    /// A program ended itself with this exit code (`program::run`).
    Exited(i32),
}

/// Where the functions' addresses start: above all data, inside 32 bits,
/// so no load or store reaches one and every target's pointers hold it.
const FUNCTIONS_START: u64 = 1 << 31;
/// How far apart two functions' addresses are.
const FUNCTION_STRIDE: u64 = 16;

/// What `function`, whose MIR is `body`, returns for `arguments`, each a
/// value with the shape of its parameter's type, for `target`: `None` for a
/// function without a result, which has the shape `result` otherwise. A
/// function it calls has the MIR `mir_of` gives, if any.
pub fn run_function<'b>(
    function: DefId,
    body: &'b Body,
    arguments: &[(&Value, ConstantShape)],
    result: ConstantShape,
    target: &Machine,
    mir_of: &dyn Fn(FunctionCopy) -> Option<&'b Body>,
) -> Result<Option<Value>, Failure> {
    let mut run = Interpreter::new(target, mir_of, Memory::for_constant());
    let span = body.blocks.span(Id::from_index(0));
    let failed = |message: &str| Failure::Stopped {
        message: message.to_owned(),
        function,
        span,
    };
    // A result kept in memory is written where the hidden first argument
    // points.
    let kept_at = match body.signature.result {
        Some(Passing::Memory { size, .. }) => {
            let at = run.memory.open_block(size);
            let at = at.ok_or_else(|| failed(OUT_OF_STACK))?;
            run.registers.push(u128::from(at));
            Some(at)
        }
        _ => None,
    };
    let passings = body.signature.parameters.iter();
    for (&(argument, shape), &passing) in arguments.iter().zip(passings) {
        let register = run.register_of(argument, passing, shape);
        let register = register.ok_or_else(|| failed(OUT_OF_STACK))?;
        run.registers.push(register);
    }
    let (value, returned_at) = run.run_from(function, body).map_err(|halt| match halt {
        Halt::Failed(failure) => failure,
        Halt::Exited(_) => unreachable!("only a program's C call exits"),
    })?;
    let value = match (value, kept_at) {
        (_, Some(at)) => u128::from(at),
        (Some(value), None) => value,
        (None, None) => return Ok(None),
    };
    run.constant_of(value, result)
        .map(Some)
        .map_err(|message| Failure::Stopped {
            message: message.to_owned(),
            function,
            span: returned_at,
        })
}

/// Where a body keeps its slots, from the start of its frame, and where its
/// globals are: worked out once for a run.
struct Layout {
    slots: Vec<u64>,
    size: u64,
    globals: Vec<u64>,
}

/// One running call.
struct Frame<'b> {
    function: DefId,
    body: &'b Body,
    layout: Rc<Layout>,
    start: u64, // where the room for its frame starts; a program keeps its slots there
    /// In a constant's run, where each slot is now, if it started; empty in
    /// a program's, whose slots stay at `layout.slots` from `start`.
    slots: Vec<Option<NonZeroU64>>,
    /// The caller's `first_register` and `first_value`, put back on return.
    caller_first_register: usize,
    caller_first_value: usize,
    block: Id<Block>,
    next: usize,                     // how many of the block's instructions have run
    result: Option<Id<Instruction>>, // the call in the frame below
}

struct Interpreter<'b, 'f> {
    target: &'f Machine,
    mir_of: &'f dyn Fn(FunctionCopy) -> Option<&'b Body>,
    steps: u64, // run so far, by every call; past the limit the run stops, so it cannot wrap
    host: Host, // what a program printed; a constant's call prints nothing
    memory: Memory,
    /// Each function called so far: its body and layout, or `None` when
    /// `mir_of` gives it no body; asked once each.
    called: FxHashMap<FunctionCopy, Option<(&'b Body, Rc<Layout>)>>,
    /// The functions whose addresses a table holds, in the order of their
    /// addresses from `FUNCTIONS_START`.
    functions: Vec<FunctionCopy>,
    frames: Vec<Frame<'b>>,
    /// Of every frame, the running one last. A register holds an integer
    /// of up to 128 bits, a float's bits or an address, with the bits above
    /// its class zero; one of the class `Wide` holds where in `wides` its
    /// value is.
    registers: Vec<u128>,
    /// The value of each register of the class `Wide`, at the register's
    /// own index (`hold`), so a loop writes over its last value.
    wides: Vec<Limbs>,
    first_register: usize, // where the running call's arguments start
    first_value: usize,    // where its instructions' values start, after its arguments
}

impl<'b, 'f> Interpreter<'b, 'f> {
    fn new(
        target: &'f Machine,
        mir_of: &'f dyn Fn(FunctionCopy) -> Option<&'b Body>,
        memory: Memory,
    ) -> Self {
        Interpreter {
            target,
            mir_of,
            steps: 0,
            memory,
            host: Host::default(),
            called: FxHashMap::default(),
            functions: Vec::new(),
            frames: Vec::new(),
            registers: Vec::new(),
            wides: Vec::new(),
            first_register: 0,
            first_value: 0,
        }
    }

    /// Runs `function`, whose arguments are the registers so far, until it
    /// returns: what it returned, and the `return` or `}` it returned at.
    fn run_from(&mut self, function: DefId, body: &'b Body) -> Result<(Option<u128>, Span), Halt> {
        let span = body.blocks.span(Id::from_index(0));
        let start = memory::STACK_START;
        let layout = Rc::new(self.layout_of(body));
        self.push_frame(function, body, layout, None, start, 0, span)?;
        self.run_frames()
    }

    fn frame(&self) -> &Frame<'b> {
        self.frames.last().expect("a run has a frame")
    }

    fn frame_mut(&mut self) -> &mut Frame<'b> {
        self.frames.last_mut().expect("a run has a frame")
    }

    /// The run stopped at `span` with `message`.
    fn fail(&self, message: impl Into<String>, span: Span) -> Halt {
        Halt::Failed(Failure::Stopped {
            message: message.into(),
            function: self.frame().function,
            span,
        })
    }

    /// How many bits a register of `class` holds; a float's are its bits.
    fn class_bits(&self, class: Class) -> u32 {
        match class {
            Class::Pointer => self.target.pointer_bits,
            class => class.bits(),
        }
    }

    /// Where the running call's instruction `id` is written: looked up
    /// only when the run stops there.
    fn span_of(&self, id: Id<Instruction>) -> Span {
        self.frame().body.instructions.span(id)
    }

    /// Counts `steps` more steps: an error past the limit of a constant's
    /// call or of a program's run.
    fn count_steps(&mut self, steps: u64) -> Result<(), String> {
        self.steps += steps;
        let limit = if self.memory.is_constant() {
            CONSTANT_STEP_LIMIT
        } else {
            PROGRAM_STEP_LIMIT
        };
        if self.steps > u64::from(limit) {
            return Err(format!("this ran more than {limit} steps"));
        }
        Ok(())
    }

    /// Counts the work of `bytes` bytes copied by the instruction `id`.
    fn count_copy(&mut self, bytes: u64, id: Id<Instruction>) -> Result<(), Halt> {
        let counted = self.count_steps(bytes / BYTES_PER_STEP);
        counted.map_err(|message| self.fail(message, self.span_of(id)))
    }

    /// The body and layout of `function`, the first time it is called
    /// worked out and kept; `None` when it has no body.
    fn called(&mut self, function: FunctionCopy) -> Option<(&'b Body, Rc<Layout>)> {
        if let Some(called) = self.called.get(&function) {
            return called.clone();
        }
        let called = (self.mir_of)(function).map(|body| (body, Rc::new(self.layout_of(body))));
        self.called.insert(function, called.clone());
        called
    }

    /// Where the slots and globals of `body` are, its globals placed in
    /// the run's data.
    fn layout_of(&mut self, body: &'b Body) -> Layout {
        let mut size = 0u64;
        let slots = (body.slots.ids())
            .map(|id| {
                let slot = &body.slots[id];
                let offset = size.next_multiple_of(slot.align);
                size = offset.saturating_add(slot.size);
                offset
            })
            .collect();
        let pointer = self.target.pointer_bytes() as usize;
        let globals = (body.globals.iter())
            .map(|global| match global {
                Global::Bytes(bytes) => self.memory.string_address(bytes),
                Global::Functions(functions) => {
                    let mut table = Vec::with_capacity(functions.len() * pointer);
                    for &(function, _) in functions {
                        let address = self.function_address(function);
                        table.extend_from_slice(&address.to_le_bytes()[..pointer]);
                    }
                    self.memory.string_address(&table)
                }
            })
            .collect();
        Layout {
            slots,
            size,
            globals,
        }
    }

    /// The address of `function`, given it the first time.
    fn function_address(&mut self, function: FunctionCopy) -> u64 {
        let known = self.functions.iter().position(|&known| known == function);
        let index = known.unwrap_or_else(|| {
            self.functions.push(function);
            self.functions.len() - 1
        });
        FUNCTIONS_START + index as u64 * FUNCTION_STRIDE
    }

    /// Starts a call of `function`, of the body `body` laid out as
    /// `layout`, whose value goes to `result` of the frame below, with
    /// `end` where the calling frame's memory stops and its arguments the
    /// registers from `first_register` on.
    #[expect(clippy::too_many_arguments, reason = "each is one fact of the call")]
    fn push_frame(
        &mut self,
        function: DefId,
        body: &'b Body,
        layout: Rc<Layout>,
        result: Option<Id<Instruction>>,
        end: u64,
        first_register: usize,
        span: Span,
    ) -> Result<(), Halt> {
        if self.frames.len() == CALL_LIMIT {
            let message = format!("this went {CALL_LIMIT} calls deep");
            return Err(self.fail(message, span));
        }
        let first_value = self.registers.len();
        let used = first_value + body.instructions.len();
        // A call holds room for its whole frame, as on a machine, so too
        // big a frame stops the call; a constant's run gives each slot its
        // own block too, each time it starts (`slot_address`).
        let start = self.memory.enter_call(end, layout.size);
        let Some(start) = start.filter(|_| used <= REGISTER_LIMIT) else {
            return Err(Halt::Failed(Failure::Stopped {
                message: OUT_OF_STACK.to_owned(),
                function: self.frames.last().map_or(function, |frame| frame.function),
                span,
            }));
        };
        self.registers.resize(used, 0);
        let slots = if self.memory.is_constant() {
            vec![None; body.slots.len()]
        } else {
            Vec::new()
        };
        self.frames.push(Frame {
            function,
            body,
            layout,
            start,
            slots,
            caller_first_register: self.first_register,
            caller_first_value: self.first_value,
            block: Id::from_index(0),
            next: 0,
            result,
        });
        self.first_register = first_register;
        self.first_value = first_value;
        Ok(())
    }

    /// Runs until the first frame returns, giving what it returned and
    /// where.
    fn run_frames(&mut self) -> Result<(Option<u128>, Span), Halt> {
        'frames: loop {
            let frame = self.frame();
            let (body, block_id, done) = (frame.body, frame.block, frame.next);
            let block = &body.blocks[block_id];
            let instructions = body.lists.items(block.instructions);
            for (index, id) in instructions.enumerate().skip(done) {
                let counted = self.count_steps(1);
                counted.map_err(|message| self.fail(message, self.span_of(id)))?;
                if let InstructionKind::Call {
                    callee,
                    signature,
                    arguments,
                    result,
                } = &body.instructions[id].kind
                {
                    self.frame_mut().next = index + 1;
                    let result = result.as_ref().map(|(place, _)| place);
                    self.call(id, *callee, *signature, result, arguments)?;
                    continue 'frames;
                }
                if let Some(value) = self.run_instruction(id)? {
                    self.set_value(id, value);
                }
            }
            let span = match block.terminator {
                Terminator::Branch { span, .. }
                | Terminator::Return { span, .. }
                | Terminator::Panic { span, .. }
                | Terminator::FallOff(span) => span,
                Terminator::Jump(_) => body.blocks.span(block_id),
            };
            self.count_steps(1)
                .map_err(|message| self.fail(message, span))?;
            let next = match &block.terminator {
                &Terminator::Jump(target) => target,
                &Terminator::Branch {
                    condition,
                    then,
                    otherwise,
                    ..
                } => match self.operand(condition) {
                    0 => otherwise,
                    _ => then,
                },
                &Terminator::Return { value, .. } => {
                    let value = value.map(|value| self.operand(value));
                    match self.finish_call(value) {
                        Some(returned) => return Ok((returned, span)),
                        None => continue,
                    }
                }
                Terminator::Panic { message, .. } => {
                    return Err(self.fail(String::from_utf8_lossy(message), span));
                }
                &Terminator::FallOff(_) => match self.finish_call(None) {
                    Some(returned) => return Ok((returned, span)),
                    None => continue,
                },
            };
            let frame = self.frame_mut();
            frame.block = next;
            frame.next = 0;
        }
    }

    /// Calls `callee`, of the signature `signature` of the running body,
    /// for its instruction `id`, writing a result kept in memory to
    /// `result`.
    fn call(
        &mut self,
        id: Id<Instruction>,
        callee: Callee,
        signature: u32,
        result: Option<&Place>,
        arguments: &[Operand],
    ) -> Result<(), Halt> {
        let copy = match callee {
            Callee::Function(copy) => copy,
            Callee::Runtime(runtime) => return self.call_runtime(runtime, arguments, id),
            Callee::Pointer(pointer) => self.function_at(pointer, signature, id)?,
        };
        let Some((body, layout)) = self.called(copy) else {
            if !self.memory.is_constant() {
                return self.call_outside(id, copy, signature, arguments);
            }
            return Err(Halt::Failed(Failure::NoMir {
                callee: copy,
                function: self.frame().function,
                span: self.span_of(id),
            }));
        };
        let frame = self.frame();
        let end = frame.start + frame.layout.size;
        // Its address is the hidden first argument (`Root::Result`).
        let result_at = match result {
            Some(place) => Some(self.address(place, id)?),
            None => None,
        };
        let first_register = self.registers.len();
        self.registers.extend(result_at.map(u128::from));
        for &argument in arguments {
            let register = self.operand(argument);
            self.registers.push(register);
        }
        let definition = copy.definition;
        self.push_frame(
            definition,
            body,
            layout,
            Some(id),
            end,
            first_register,
            self.span_of(id),
        )
    }

    /// The function at the address `pointer` gives, called with the
    /// signature `signature` of the running body: an error for an address
    /// that is no function's, or one the call does not fit, which only a
    /// raw pointer's write over a table can make.
    fn function_at(
        &mut self,
        pointer: Operand,
        signature: u32,
        id: Id<Instruction>,
    ) -> Result<FunctionCopy, Halt> {
        let address = self.operand(pointer) as u64;
        let index = (address.checked_sub(FUNCTIONS_START))
            .filter(|offset| offset % FUNCTION_STRIDE == 0)
            .map(|offset| (offset / FUNCTION_STRIDE) as usize);
        let function = index.and_then(|index| self.functions.get(index).copied());
        let running = self.frame().body;
        let expected = &running.signatures[signature as usize];
        let fits = |(body, _): (&Body, Rc<Layout>)| fits(expected, &body.signature);
        match function {
            Some(function) if self.called(function).is_some_and(fits) => Ok(function),
            _ => Err(self.fail(INVALID_ACCESS, self.span_of(id))),
        }
    }

    /// Ends the running call with `value`, and gives what it returned if
    /// it was the first.
    fn finish_call(&mut self, value: Option<u128>) -> Option<Option<u128>> {
        let frame = self.frames.pop().expect("a run has a frame");
        let first_register = self.first_register;
        self.first_register = frame.caller_first_register;
        self.first_value = frame.caller_first_value;
        for start in frame.slots.iter().flatten() {
            self.memory.close_block(start.get());
        }
        self.memory.leave_call(frame.start);
        let Some(result) = frame.result else {
            return Some(value);
        };
        self.registers.truncate(first_register);
        if let Some(mut value) = value {
            // A wide value moves to the caller's register, as the callee's
            // index may be taken by the next call.
            if let Some(Passing::Register { class, .. }) = frame.body.signature.result
                && let Class::Wide(_) = class
            {
                let limbs = self.wides[value as usize].clone();
                let wide = RegisterValue::Wide(Cow::Owned(limbs));
                value = self.hold(self.first_value + result.index(), wide);
            }
            self.set_value(result, value);
        }
        None
    }

    fn set_value(&mut self, id: Id<Instruction>, value: u128) {
        let at = self.first_value + id.index();
        self.registers[at] = value;
    }

    /// The register `operand` gives. A constant is never of the class
    /// `Wide`: lowering loads a wide one from read-only data.
    fn operand(&self, operand: Operand) -> u128 {
        match operand {
            Operand::Value(id) => self.registers[self.first_value + id.index()],
            Operand::Constant { bits, .. } => constant_value(bits),
            Operand::Parameter(index) => {
                // After the address of a result kept in memory (`Root::Result`).
                let hidden = self.frame().body.signature.hidden_parameters() as usize;
                self.registers[self.first_register + hidden + index as usize]
            }
            Operand::Global(index) => u128::from(self.frame().layout.globals[index as usize]),
        }
    }

    /// Where `slot` of the running call is. In a constant's run it gets a
    /// new block each time it starts, so an address of the last one, from
    /// an earlier pass of a loop, cannot reach this one.
    fn slot_address(&mut self, slot: Id<Slot>, id: Id<Instruction>) -> Result<u64, Halt> {
        let frame = self.frame();
        if let Some(parameter) = frame.body.parameter_in_memory(slot) {
            return Ok(self.operand(Operand::Parameter(parameter)) as u64);
        }
        if !self.memory.is_constant() {
            return Ok(frame.start + frame.layout.slots[slot.index()]);
        }
        if let Some(started) = frame.slots[slot.index()] {
            return Ok(started.get());
        }
        let size = frame.body.slots[slot].size;
        let Some(start) = self.memory.open_block(size) else {
            return Err(self.fail(OUT_OF_STACK, self.span_of(id)));
        };
        let started = NonZeroU64::new(start).expect("a block starts above the null address");
        self.frame_mut().slots[slot.index()] = Some(started);
        Ok(start)
    }

    /// The register `value`, of the shape `shape`, is, passed as `passing`:
    /// `None` when a value kept in memory finds no room.
    fn register_of(
        &mut self,
        value: &Value,
        passing: Passing,
        shape: ConstantShape,
    ) -> Option<u128> {
        let pointer = self.target.pointer_bytes() as usize;
        Some(match (value, passing) {
            (Value::Bool(value), _) => u128::from(*value),
            // Extended to its class, as its type's values are kept.
            (Value::Integer(limbs), Passing::Register { class, .. }) => {
                let ConstantShape::Integer { bits, signed } = shape else {
                    unreachable!("an integer constant has an integer type")
                };
                let to = class.bits();
                let extended = bigint::extend(bits, signed, limbs, to);
                // Its register is the next one pushed.
                let value = RegisterValue::of_limbs(to, Cow::Owned(extended));
                self.hold(self.registers.len(), value)
            }
            (Value::Float(bits), Passing::Register { class, .. }) => {
                operations::float_bits(class, f64::from_bits(*bits))
            }
            (Value::Null, _) => 0,
            (Value::Bytes(text), Passing::Register { .. }) => {
                u128::from(self.memory.string_address(text))
            }
            // A `&str`: its bytes' address and count, in memory.
            (Value::Bytes(text), Passing::Memory { size, .. }) => {
                let address = self.memory.string_address(text);
                let at = self.memory.open_block(size)?;
                let words = self.memory.bytes_mut(at, size)?;
                words[..pointer].copy_from_slice(&address.to_le_bytes()[..pointer]);
                let length = text.len() as u64;
                words[pointer..].copy_from_slice(&length.to_le_bytes()[..pointer]);
                u128::from(at)
            }
            _ => unreachable!("a constant is a number, `bool`, string or `none`"),
        })
    }

    /// The constant of the shape `shape` the run gave in `bits`: an address
    /// for a value kept in memory. `Err` says why it cannot be one.
    fn constant_of(&self, bits: u128, shape: ConstantShape) -> Result<Value, &'static str> {
        let pointer = self.target.pointer_bytes();
        let word = |at: u64| -> Result<u64, &'static str> {
            let bytes = self.memory.bytes(at, pointer);
            let bytes = bytes.ok_or(INVALID_ACCESS)?;
            let mut word = [0; 8];
            word[..pointer as usize].copy_from_slice(bytes);
            Ok(u64::from_le_bytes(word))
        };
        Ok(match shape {
            ConstantShape::Bool => Value::Bool(bits != 0),
            ConstantShape::Integer { bits: width, .. } => match Class::integer(width) {
                Class::Wide(_) => Value::Integer(self.wides[bits as usize].clone()),
                _ => Value::Integer(RegisterValue::Narrow(bits).limbs(width).into_owned()),
            },
            ConstantShape::Float(class) => {
                Value::Float(operations::float_value(class, bits).to_bits())
            }
            ConstantShape::RawPointer if bits == 0 => Value::Null,
            ConstantShape::RawPointer => return Err(ADDRESS_NOT_KNOWN),
            ConstantShape::CString => match self.memory.c_text(bits as u64) {
                Some(text) => Value::Bytes(text.into()),
                None => return Err(INVALID_ACCESS),
            },
            ConstantShape::Str => {
                let (address, length) = (word(bits as u64)?, word(bits as u64 + pointer)?);
                let text = self.memory.bytes(address, length);
                Value::Bytes(text.ok_or(INVALID_ACCESS)?.into())
            }
            ConstantShape::Refused(message) => return Err(message),
        })
    }
}

/// Whether a call of the signature `called` fits a body of the signature
/// `body`: the same registers in and out, a value kept in memory passed as
/// its address. A method's receiver is an address at the call, as the
/// struct behind it differs by implementation, and the struct itself in the
/// body of one that takes `self` whole.
fn fits(called: &Signature, body: &Signature) -> bool {
    let class = |passing: &Passing| passing.register_class();
    let parameters = called.parameters.iter().map(class);
    called.hidden_parameters() == body.hidden_parameters()
        && called.result.as_ref().map(class) == body.result.as_ref().map(class)
        && parameters.eq(body.parameters.iter().map(class))
}
