//! Running a whole program, with no linker or system: what the runtime
//! library and C do for a compiled program is done here. `print`'s runtime
//! functions keep their text and flush it, `write` adds to the output, and
//! the library's functions below `core` are answered directly. Output and
//! memory stay inside the run, so a program can do nothing to the machine it
//! runs on.

use arena::Id;
use model::DefId;
use model::FunctionCopy;
use model::machine::Machine;
use model::mir::Body;
use model::mir::Instruction;
use model::mir::Operand;
use model::mir::Passing;
use runtime_abi::abi::STDERR;
use runtime_abi::abi::STDIN;
use runtime_abi::abi::STDOUT;
use runtime_abi::number_text;
use runtime_abi::symbols::ALLOC;
use runtime_abi::symbols::EXIT;
use runtime_abi::symbols::FREE;
use runtime_abi::symbols::READ;
use runtime_abi::symbols::REALLOC;
use runtime_abi::symbols::Runtime;
use runtime_abi::symbols::WRITE;

use super::Failure;
use super::Halt;
use super::INVALID_ACCESS;
use super::Interpreter;
use super::memory::Memory;
use crate::operations::mask;

/// How many steps a program may run: more than any page should wait for, so a
/// page's own time limit usually ends a program first.
const PROGRAM_STEP_LIMIT: u32 = 2_000_000_000;

/// How many bytes a program may print, so one that prints forever stops
/// before its output fills the host's memory.
const OUTPUT_LIMIT: u64 = 1 << 20;

/// How many pieces a program's output may have, so one that keeps changing
/// streams cannot give a page a million of them to show.
const PIECE_LIMIT: usize = 10_000;

/// Which of its outputs a program wrote to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Stream {
    Stdout,
    Stderr,
}

/// What a program wrote to one `stream` before it wrote to the other.
pub struct Written {
    pub stream: Stream,
    pub text: Vec<u8>,
}

/// What a program did: what it printed, in the order it did, and the code it
/// exited with, or why it stopped.
pub struct Outcome {
    pub output: Vec<Written>,
    pub ending: Result<i32, Failure>,
}

/// Runs the program that starts at `main`, whose MIR is `body`, for
/// `target`. A function it calls has the MIR `mir_of` gives, or is a C
/// function outside the program, named by its call's C signature.
pub fn run<'b>(
    main: DefId,
    body: &'b Body,
    target: &Machine,
    mir_of: &dyn Fn(FunctionCopy) -> Option<&'b Body>,
) -> Outcome {
    let mut run = Interpreter::new(target, mir_of, PROGRAM_STEP_LIMIT, Memory::for_program());
    let ending = match run.run_from(main, body) {
        Ok((Some(code), _)) => Ok(code as u32 as i32),
        Ok(_) => Ok(0),
        Err(Halt::Exited(code)) => Ok(code),
        Err(Halt::Failed(failure)) => Err(failure),
    };
    Outcome {
        output: run.host.output,
        ending,
    }
}

/// What a program printed; a constant's call prints nothing.
#[derive(Default)]
pub(super) struct Host {
    pub(super) output: Vec<Written>,
    printed: u64, // bytes in `output`, kept as output can have a piece per print
    /// What `print` has written and not flushed, kept as the runtime keeps
    /// it: a program that stops in the middle of a `print` loses it.
    kept: Vec<u8>,
}

impl<'f> Interpreter<'_, 'f> {
    /// Stops the run if `pending` more bytes of output pass the limit, before
    /// they are made.
    fn check_output(&mut self, pending: u64, id: Id<Instruction>) -> Result<(), Halt> {
        let host = &self.host;
        let total = host.printed + host.kept.len() as u64;
        if total.saturating_add(pending) > OUTPUT_LIMIT {
            let limit = OUTPUT_LIMIT;
            return Err(self.fail(
                format!("this printed more than {limit} bytes"),
                self.span_of(id),
            ));
        }
        Ok(())
    }

    /// Adds `text` to the output of the descriptor `fd`: false for one that
    /// is neither of a program's outputs.
    fn write_output(&mut self, fd: i32, text: &[u8], id: Id<Instruction>) -> Result<bool, Halt> {
        self.count_copy(text.len() as u64, id)?;
        self.check_output(text.len() as u64, id)?;
        let stream = match fd {
            STDOUT => Stream::Stdout,
            STDERR => Stream::Stderr,
            _ => return Ok(false),
        };
        let host = &mut self.host;
        host.printed += text.len() as u64;
        let full = host.output.len() >= PIECE_LIMIT;
        match host.output.last_mut() {
            // Once full, a stream's text can sit in the other's piece, but all stays in order.
            Some(last) if last.stream == stream || full => last.text.extend_from_slice(text),
            _ => host.output.push(Written {
                stream,
                text: text.to_vec(),
            }),
        }
        Ok(true)
    }

    /// Keeps `text` for `print`'s next flush.
    fn keep_output(&mut self, text: &[u8], id: Id<Instruction>) -> Result<(), Halt> {
        self.count_copy(text.len() as u64, id)?;
        self.check_output(text.len() as u64, id)?;
        self.host.kept.extend_from_slice(text);
        Ok(())
    }

    /// Runs the runtime function `runtime`, which only `print` calls, with
    /// `arguments`, for the call `id`.
    pub(super) fn call_runtime(
        &mut self,
        runtime: Runtime,
        arguments: &[Operand],
        id: Id<Instruction>,
    ) -> Result<(), Halt> {
        if self.memory.is_constant() {
            return Err(self.fail(
                "`print` calls cannot run at compile time yet",
                self.span_of(id),
            ));
        }
        let argument = |run: &Self, index: usize| run.operand(arguments[index]) as u64;
        let mut text = Vec::new();
        let mut push = |piece: &[u8]| text.extend_from_slice(piece);
        match runtime {
            Runtime::PrintBytes => {
                let (address, length) = (argument(self, 0), argument(self, 1));
                self.check_output(length, id)?;
                push(self.read_bytes(address, length, id)?);
            }
            Runtime::PrintText => match argument(self, 0) {
                0 => push(b"null"),
                address => match self.memory.c_text(address) {
                    Some(bytes) => push(bytes),
                    None => return Err(self.fail(INVALID_ACCESS, self.span_of(id))),
                },
            },
            Runtime::PrintInt => {
                let (signed, count) = (argument(self, 0) != 0, argument(self, 1) as usize);
                let bytes = self.read_bytes(argument(self, 2), count as u64 * 8, id)?;
                let mut limbs: Vec<u64> = (bytes.chunks(8))
                    .map(|limb| u64::from_le_bytes(limb.try_into().expect("eight bytes a limb")))
                    .collect();
                let mut chunks = vec![0; number_text::max_chunks(count)];
                number_text::write_integer(signed, &mut limbs, &mut chunks, &mut push);
            }
            Runtime::PrintFloat => {
                let (value, bits) = (argument(self, 0), argument(self, 1));
                number_text::write_float(f64::from_bits(value), bits as i32, &mut push);
            }
            Runtime::PrintPointer => number_text::write_pointer(argument(self, 0), &mut push),
            Runtime::Flush => {
                let kept = std::mem::take(&mut self.host.kept);
                self.write_output(STDOUT, &kept, id)?;
                return Ok(());
            }
            Runtime::PanicWrite | Runtime::PanicExit => {
                unreachable!("the LLVM writer writes a panic's calls; lowering makes none")
            }
        }
        self.keep_output(&text, id)
    }

    /// The integer argument `index` of the call of the signature
    /// `signature`, as C reads a size: `None` for a negative one.
    fn size_argument(&self, signature: u32, arguments: &[Operand], index: usize) -> Option<u64> {
        let body = self.frame().body;
        let Passing::Register { class, signed } =
            body.signatures[signature as usize].parameters[index]
        else {
            unreachable!("a C size is passed in a register")
        };
        let bits = self.class_bits(class);
        let value = self.operand(arguments[index]);
        let negative = signed && value >> (bits - 1) & 1 != 0;
        (!negative).then_some(value as u64)
    }

    /// The `i32` argument `index`.
    fn i32_argument(&self, arguments: &[Operand], index: usize) -> i32 {
        self.operand(arguments[index]) as u32 as i32
    }

    /// A block of `size` bytes as a pointer: null when there is no room, as
    /// `malloc` says so, which goes on. A block made costs as copying `size`
    /// bytes does.
    fn allocated(&mut self, size: u64, id: Id<Instruction>) -> Result<u128, Halt> {
        let Some(address) = self.memory.allocate(size) else {
            return Ok(0);
        };
        self.count_copy(size, id)?;
        Ok(u128::from(address))
    }

    /// Runs the function `callee` that is outside the program, for the
    /// call `id` of the signature `signature`: what the runtime library and
    /// C give every program. Anything else needs a system this run does not
    /// have.
    pub(super) fn call_outside(
        &mut self,
        id: Id<Instruction>,
        callee: FunctionCopy,
        signature: u32,
        arguments: &[Operand],
    ) -> Result<(), Halt> {
        let c = &self.frame().body.signatures[signature as usize].c;
        let Some(symbol) = c.as_ref().map(|c| c.symbol.clone()) else {
            return Err(Halt::Failed(Failure::NoMir {
                callee,
                function: self.frame().function,
                span: self.span_of(id),
            }));
        };
        let size = |run: &Self, index| run.size_argument(signature, arguments, index);
        // A size past every block's: one that is negative, or too large.
        let any_size = |run: &Self, index| size(run, index).unwrap_or(u64::MAX);
        let address = |run: &Self, index: usize| run.operand(arguments[index]) as u64;
        let result = match (&*symbol, arguments.len()) {
            (ALLOC | "malloc", 1) => Some(self.allocated(any_size(self, 0), id)?),
            ("calloc", 2) => {
                let (count, each) = (any_size(self, 0), any_size(self, 1));
                Some(match count.checked_mul(each) {
                    Some(bytes) => {
                        let block = self.allocated(bytes, id)?;
                        if let address @ 1.. = block
                            && let Some(bytes) = self.memory.bytes_mut(address as u64, bytes)
                        {
                            bytes.fill(0);
                        }
                        block
                    }
                    None => 0,
                })
            }
            (REALLOC | "realloc", 2) => {
                let (block, size) = (address(self, 0), any_size(self, 1));
                if block == 0 {
                    Some(self.allocated(size, id)?)
                } else {
                    let Some(moved) = self.memory.reallocate(block, size) else {
                        return Err(self.fail(
                            "this resizes memory that is not allocated",
                            self.span_of(id),
                        ));
                    };
                    self.count_copy(size, id)?;
                    Some(u128::from(moved.unwrap_or(0)))
                }
            }
            (FREE | "free", 1) => {
                let block = address(self, 0);
                if block != 0 && self.memory.free(block).is_none() {
                    return Err(
                        self.fail("this frees memory that is not allocated", self.span_of(id))
                    );
                }
                None
            }
            // A negative length gives -1, as the runtime refuses it.
            (WRITE | READ, 3) if size(self, 2).is_none() => Some(u128::MAX),
            (WRITE, 3) => {
                let (fd, buffer) = (self.i32_argument(arguments, 0), address(self, 1));
                let length = any_size(self, 2);
                self.check_output(length, id)?;
                let bytes = self.read_bytes(buffer, length, id)?.to_vec();
                let written = self.write_output(fd, &bytes, id)?;
                Some(if written {
                    u128::from(length)
                } else {
                    u128::MAX
                })
            }
            // No program here has an input, so its stream is always at its end.
            (READ, 3) => {
                let at_end = self.i32_argument(arguments, 0) == STDIN;
                Some(if at_end { 0 } else { u128::MAX })
            }
            (EXIT, 1) => return Err(Halt::Exited(self.i32_argument(arguments, 0))),
            (symbol, _) => {
                let message = format!("`{symbol}` is outside Peeper, so it cannot run here");
                return Err(self.fail(message, self.span_of(id)));
            }
        };
        // Cut to the class the call gives.
        if let Some(result) = result {
            let body = self.frame().body;
            let Some(Passing::Register { class, .. }) = body.signatures[signature as usize].result
            else {
                unreachable!("these functions give a register")
            };
            self.set_value(id, result & mask(self.class_bits(class)));
        }
        Ok(())
    }
}
