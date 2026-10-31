//! Running a function's MIR, with no backend: what a constant whose value
//! needs a call is. Each operation means what `binary`, `unary` and `convert`
//! say, as when check folds, so a function gives the same result here as when
//! the program runs.
//!
//! Variables, and values kept in memory, live in `memory::Memory`, laid out
//! as for the target (`model::layout`), so references, slices and raw
//! pointers are real addresses. Other values are held whole in `Register`s,
//! and a call is a `Frame` on a list, so a program's recursion uses no Rust
//! stack and the run ends with an error before any limit of the host.

mod aggregates;
mod calls;
mod instructions;
mod memory;
mod places;
pub mod program;
mod values;

use std::collections::HashMap;
use std::rc::Rc;

use arena::Id;
use model::DefId;
use model::constant::Value;
use model::layout::layout;
use model::machine::Machine;
use model::mir;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use source::Span;

use memory::Memory;
use program::Host;

/// How many instructions and terminators one evaluation may run, so an
/// endless loop stops with an error instead of hanging the compiler or an
/// editor. Work that one instruction repeats counts too: a value of a filled
/// range is a step, and so are `BYTES_PER_STEP` bytes copied.
const STEP_LIMIT: u32 = 1_000_000;

/// How many bytes a copy moves for each step it counts as: about what one
/// step costs, as copying is far faster than running an instruction.
const BYTES_PER_STEP: u64 = 256;

/// How many calls may be running at once. A recursion that never ends meets
/// this or `REGISTER_LIMIT` long before `STEP_LIMIT`, as each call is a few
/// steps.
const CALL_LIMIT: usize = 100_000;

/// How many arguments and values all running calls may hold, so a large
/// function that recurses cheaply stops before the host runs out of memory.
const REGISTER_LIMIT: usize = 1 << 20;

const INVALID_ACCESS: &str = "this reads or writes memory the program does not own";

/// Why a constant's run stops where it would read an address, not only use one.
const ADDRESS_NOT_KNOWN: &str = "an address is known only when the program runs";

const OUT_OF_STACK: &str = "this ran out of stack";

/// Why a constant cannot hold a value of the type `ty`.
pub fn constant_refusal(ty: &Type) -> &'static str {
    match ty {
        Type::Struct { .. } => "a constant cannot hold a struct yet",
        Type::Array { .. } => "a constant cannot hold an array yet",
        Type::Range { .. } => "a constant cannot hold a range yet",
        Type::Reference { mutable: true, .. } | Type::Slice { mutable: true, .. } => {
            "a constant cannot hold a mutable reference, as all its uses would share one value"
        }
        _ => ADDRESS_NOT_KNOWN,
    }
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
    /// It called `callee`, for which `mir_of` gave no MIR: the caller of
    /// `run` knows why.
    NoMir {
        callee: DefId,
        function: DefId,
        span: Span,
    },
    /// A program ended itself with this exit code (`program::run`).
    Exited(i32),
}

/// What `function`, whose MIR is `body`, returns for `arguments`, laid out
/// for `target`: `None` for a function without a result. A function it calls
/// has the MIR `mir_of` gives, if any.
pub fn run<'b>(
    function: DefId,
    body: &'b mir::Body,
    arguments: &[Value],
    types: &'b Types,
    target: &'b Machine,
    mir_of: &dyn Fn(DefId) -> Option<&'b mir::Body>,
) -> Result<Option<Value>, Failure> {
    let mut machine = Interpreter::new(types, target, mir_of, STEP_LIMIT, None);
    for argument in arguments {
        let register = machine.register_of(argument);
        machine.registers.push(Some(register));
    }
    machine.run_from(function, body)
}

/// A value being used. A pointer, a reference and a `cstr` are addresses; a
/// value `in_memory` is the address it is kept at.
#[derive(Clone, Debug)]
enum Register {
    Scalar(Value),
    Address(u64),
    Slice { address: u64, length: u64 },
}

/// Where one body keeps its values, from the start of its frame.
struct Slots {
    variables: Vec<u64>,
    instructions: Vec<u64>, // by instruction; only a value `in_memory` has one
    size: u64,
}

/// One running call.
struct Frame<'b> {
    function: DefId,
    body: &'b mir::Body,
    slots: Rc<Slots>,
    start: u64,          // where its memory starts
    variables: Vec<u64>, // in a constant's run, where each is now; 0 for not started
    registers: usize,    // where its arguments start, its values after them
    block: Id<mir::Block>,
    next: usize,                          // how many of the block's instructions have run
    previous: Option<Id<mir::Block>>,     // the block control came from
    result: Option<Id<mir::Instruction>>, // the call in the frame below
}

struct Interpreter<'b, 'f> {
    types: &'b Types,
    target: &'b Machine,
    mir_of: &'f dyn Fn(DefId) -> Option<&'b mir::Body>,
    steps: u32, // run so far, by every call
    step_limit: u32,
    host: Option<Host<'f>>, // a whole program's; a constant's call has none
    memory: Memory,
    slots: HashMap<*const mir::Body, Rc<Slots>>,
    fields: HashMap<TypeId, Box<[(u64, TypeId)]>>, // of a struct or range: offset and type
    frames: Vec<Frame<'b>>,
    registers: Vec<Option<Register>>, // of every frame, the running one last
}

/// Where a `Place` is: its address and type, and for a slice's elements, how
/// many there are.
struct Located {
    address: u64,
    ty: TypeId,
    length: Option<u64>,
}

impl<'b, 'f> Interpreter<'b, 'f> {
    fn new(
        types: &'b Types,
        target: &'b Machine,
        mir_of: &'f dyn Fn(DefId) -> Option<&'b mir::Body>,
        step_limit: u32,
        host: Option<Host<'f>>,
    ) -> Self {
        Interpreter {
            types,
            target,
            mir_of,
            steps: 0,
            step_limit,
            memory: if host.is_some() {
                Memory::default()
            } else {
                Memory::for_constant()
            },
            host,
            slots: HashMap::new(),
            fields: HashMap::new(),
            frames: Vec::new(),
            registers: Vec::new(),
        }
    }

    /// Runs `function`, whose arguments are the registers so far.
    fn run_from(&mut self, function: DefId, body: &'b mir::Body) -> Result<Option<Value>, Failure> {
        let span = body.blocks.span(Id::from_index(0));
        self.push_frame(function, body, None, memory::STACK_START, 0, span)?;
        self.run_frames()
    }

    fn frame(&self) -> &Frame<'b> {
        self.frames.last().expect("a run has a frame")
    }

    /// The run stopped at `span` with `message`.
    fn fail(&self, message: impl Into<String>, span: Span) -> Failure {
        Failure::Stopped {
            message: message.into(),
            function: self.frame().function,
            span,
        }
    }

    fn size_of(&self, ty: TypeId) -> u64 {
        layout(self.types, ty, self.target).0
    }

    fn pointer_bytes(&self) -> u64 {
        u64::from(self.target.pointer_bits / 8)
    }

    /// Counts one more step of the running frame, at `span`: an error past
    /// the run's limit.
    fn step(&mut self, span: Span) -> Result<(), Failure> {
        self.spend(1, span)
    }

    /// Counts a copy of `bytes` bytes.
    fn charge(&mut self, bytes: u64, span: Span) -> Result<(), Failure> {
        let steps = bytes / BYTES_PER_STEP;
        self.spend(u32::try_from(steps).unwrap_or(u32::MAX), span)
    }

    /// Counts `steps` more steps.
    fn spend(&mut self, steps: u32, span: Span) -> Result<(), Failure> {
        self.steps = self.steps.saturating_add(steps);
        if self.steps > self.step_limit {
            let message = format!("this ran more than {} steps", self.step_limit);
            return Err(self.fail(message, span));
        }
        Ok(())
    }
}
