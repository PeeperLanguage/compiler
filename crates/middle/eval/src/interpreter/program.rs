//! Running a whole program, with no linker or system: what the runtime library
//! does for a compiled program is done here. `print` and `write` add to the
//! output, and the library's functions below `core` are answered directly.
//! Output and memory stay inside the run, so a program can do nothing to the
//! machine it runs on.

use arena::Id;
use bigint::Limbs;
use model::DefId;
use model::constant::Value;
use model::layout::in_memory;
use model::machine::Machine;
use model::mir;
use model::mir::Operand;
use model::semantic_types::Float;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use source::Span;

use super::Failure;
use super::INVALID_ACCESS;
use super::Interpreter;
use super::Register;
use crate::number_text;

/// How many steps a program may run: more than any page should wait for, so a
/// page's own time limit usually ends a program first.
const STEP_LIMIT: u32 = 2_000_000_000;

/// How many bytes a program may print, so one that prints forever stops
/// before its output fills the host's memory.
const OUTPUT_LIMIT: u64 = 1 << 20;

/// How many pieces a program's output may have, so one that keeps changing
/// streams cannot give a page a million of them to show.
const PIECE_LIMIT: usize = 10_000;

/// What the standard error's and output's descriptors are, as in `core`.
const STDOUT: i32 = 1;
const STDERR: i32 = 2;

/// What a run asks of the program around its MIR, which `eval` cannot see.
pub struct ProgramFacts<'f> {
    /// The name in `#[extern("name")]` on a function.
    pub extern_symbol: &'f dyn Fn(DefId) -> Option<String>,
    /// The name of the struct a definition declares.
    pub struct_name: &'f dyn Fn(DefId) -> String,
    /// The names of its fields, in order.
    pub field_names: &'f dyn Fn(DefId) -> Vec<String>,
}

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

/// What a whole program has that a constant's call does not.
pub(super) struct Host<'f> {
    facts: &'f ProgramFacts<'f>,
    output: Vec<Written>,
    printed: u64, // bytes in `output`, kept as output can have a piece per print
}

/// Runs the program that starts at `main`, whose MIR is `body`, laid out for
/// `target`. A function it calls has the MIR `mir_of` gives, or is outside
/// the program.
pub fn run<'b>(
    main: DefId,
    body: &'b mir::Body,
    types: &'b Types,
    target: &'b Machine,
    mir_of: &dyn Fn(DefId) -> Option<&'b mir::Body>,
    facts: &ProgramFacts<'_>,
) -> Outcome {
    let host = Host {
        facts,
        output: Vec::new(),
        printed: 0,
    };
    let mut machine = Interpreter::new(types, target, mir_of, STEP_LIMIT, Some(host));
    let ending = match machine.run_from(main, body) {
        Ok(Some(Value::Integer(code))) => Ok(code.as_slice()[0] as u32 as i32),
        Ok(_) => Ok(0),
        Err(Failure::Exited(code)) => Ok(code),
        Err(failure) => Err(failure),
    };
    let host = machine.host.expect("a program's run has a host");
    Outcome {
        output: host.output,
        ending,
    }
}

impl<'f> Interpreter<'_, 'f> {
    /// The run's host, which only a program's run asks for.
    fn host(&mut self) -> &mut Host<'f> {
        self.host.as_mut().expect("a program's run has a host")
    }

    /// Stops the run if `pending` more bytes of output pass `OUTPUT_LIMIT`.
    /// It comes before the text is made, so a huge value costs nothing.
    fn check_output(&mut self, pending: u64, span: Span) -> Result<(), Failure> {
        if self.host().printed.saturating_add(pending) > OUTPUT_LIMIT {
            return Err(self.fail(format!("this printed more than {OUTPUT_LIMIT} bytes"), span));
        }
        Ok(())
    }

    /// Adds `text` to the output of the descriptor `fd`: false for one that
    /// is neither of a program's outputs.
    fn output(&mut self, fd: i32, text: &[u8], span: Span) -> Result<bool, Failure> {
        self.charge(text.len() as u64, span)?;
        self.check_output(text.len() as u64, span)?;
        let stream = match fd {
            STDOUT => Stream::Stdout,
            STDERR => Stream::Stderr,
            _ => return Ok(false),
        };
        let host = self.host();
        host.printed += text.len() as u64;
        let full = host.output.len() >= PIECE_LIMIT;
        match host.output.last_mut() {
            // Once full, a stream's text can sit in the other's piece, but all stays in order.
            Some(last) if last.stream == stream || full => {
                last.text.extend_from_slice(text);
            }
            _ => host.output.push(Written {
                stream,
                text: text.to_vec(),
            }),
        }
        Ok(true)
    }

    /// Prints `values`, a space between each, then a newline when `newline`,
    /// as the backend's `write_print` does.
    pub(super) fn print(
        &mut self,
        newline: bool,
        values: &[Operand],
        span: Span,
    ) -> Result<(), Failure> {
        let body = self.frame().body;
        let mut text = Vec::new();
        for (index, value) in values.iter().enumerate() {
            if index > 0 {
                text.push(b' ');
            }
            if let Operand::Constant(constant) = *value
                && let Value::Bytes(ref bytes) = body.constants[constant.index()].value
            {
                text.extend(bytes.split(|&byte| byte == 0).next().unwrap_or_default());
                continue;
            }
            let register = self.operand(value);
            self.write_printed(&mut text, &register, body.operand_type(*value), span)?;
        }
        if newline {
            text.push(b'\n');
        }
        self.output(STDOUT, &text, span).map(|_| ())
    }

    /// Adds the text of `value`, of the type `ty`, to `text`, as the backend's
    /// `write_printed` writes it.
    fn write_printed(
        &mut self,
        text: &mut Vec<u8>,
        value: &Register,
        ty: TypeId,
        span: Span,
    ) -> Result<(), Failure> {
        self.step(span)?;
        self.check_output(text.len() as u64, span)?;
        let types = self.types;
        match (&types[ty], value) {
            (&Type::Integer(integer), Register::Scalar(Value::Integer(limbs))) => {
                let count = integer.bits.div_ceil(64);
                let extended = bigint::extend(integer.bits, integer.signed, limbs, count * 64);
                let mut wide = extended.as_slice()[..count as usize].to_vec();
                let mut chunks = vec![0; wide.len() * 3 + 1];
                let mut push = |piece: &[u8]| text.extend_from_slice(piece);
                number_text::write_integer(integer.signed, &mut wide, &mut chunks, &mut push);
            }
            (&Type::Float(float), &Register::Scalar(Value::Float(bits))) => {
                let width = if float == Float::F32 { 32 } else { 64 };
                let mut push = |piece: &[u8]| text.extend_from_slice(piece);
                number_text::write_float(f64::from_bits(bits), width, &mut push);
            }
            (Type::Bool, &Register::Scalar(Value::Bool(value))) => {
                text.extend_from_slice(if value { b"true" } else { b"false" });
            }
            (Type::CString, &Register::Address(0)) => text.extend_from_slice(b"null"),
            (Type::CString, &Register::Address(address)) => {
                let length = self.memory.c_text(address).map(<[u8]>::len);
                let length = length.ok_or_else(|| self.fail(INVALID_ACCESS, span))?;
                self.check_output((text.len() + length) as u64, span)?;
                text.extend(self.memory.c_text(address).unwrap_or_default());
            }
            (Type::RawPointer { .. }, &Register::Address(address)) => {
                text.extend_from_slice(format!("0x{address:x}").as_bytes());
            }
            // A value kept in memory is its address, which the reference is.
            (&Type::Reference { target, .. }, value) if in_memory(types, target) => {
                self.write_printed(text, value, target, span)?;
            }
            (&Type::Reference { target, .. }, &Register::Address(address)) => {
                let referred = self.read_value(address, target, span)?;
                self.write_printed(text, &referred, target, span)?;
            }
            (
                &Type::Struct {
                    definition,
                    ref fields,
                },
                &Register::Address(address),
            ) => {
                let facts = self.host().facts;
                text.extend_from_slice((facts.struct_name)(definition).as_bytes());
                let names = (facts.field_names)(definition);
                for (index, name) in names.iter().enumerate() {
                    text.extend_from_slice(if index == 0 { b" { " } else { b", " });
                    text.extend_from_slice(name.as_bytes());
                    text.extend_from_slice(b": ");
                    let (offset, field) = self.field_place(ty, index);
                    let value = self.read_value(address + offset, field, span)?;
                    self.write_printed(text, &value, field, span)?;
                }
                text.extend_from_slice(if fields.is_empty() { b" {}" } else { b" }" });
            }
            (&Type::Array { element, length }, &Register::Address(address)) => {
                text.push(b'[');
                let size = self.size_of(element);
                for index in 0..length {
                    if index > 0 {
                        text.extend_from_slice(b", ");
                    }
                    let value = self.read_value(address + index * size, element, span)?;
                    self.write_printed(text, &value, element, span)?;
                }
                text.push(b']');
            }
            _ => unreachable!(
                "check prints only numbers, `bool`, `cstr`, pointers, structs and arrays"
            ),
        }
        Ok(())
    }

    /// The whole number the argument `operand` holds: 0 for a negative one.
    fn size_argument(&mut self, operand: &Operand) -> u64 {
        self.position(operand).unwrap_or(0)
    }

    /// The address the pointer `operand` holds.
    fn address_argument(&mut self, operand: &Operand) -> u64 {
        let Register::Address(address) = self.operand(operand) else {
            unreachable!("a pointer's register is an address")
        };
        address
    }

    /// The `i32` the argument `operand` holds.
    fn i32_argument(&mut self, operand: &Operand) -> i32 {
        let Register::Scalar(Value::Integer(limbs)) = self.operand(operand) else {
            unreachable!("an integer's register is a scalar")
        };
        limbs.as_slice()[0] as u32 as i32
    }

    /// A block of `size` bytes as a pointer: null when there is no room, as
    /// `malloc` says so. Making it costs as copying `size` bytes does.
    fn allocated(&mut self, size: u64, span: Span) -> Result<Register, Failure> {
        self.charge(size, span)?;
        Ok(Register::Address(self.memory.allocate(size).unwrap_or(0)))
    }

    /// Runs the function `function` that is outside the program, for the call
    /// `id`: what the runtime library and C give every program. Anything else
    /// needs a system this run does not have.
    pub(super) fn call_outside(
        &mut self,
        id: Id<mir::Instruction>,
        function: DefId,
        arguments: &[Operand],
    ) -> Result<(), Failure> {
        let body = self.frame().body;
        let span = body.instructions.span(id);
        let Some(symbol) = (self.host().facts.extern_symbol)(function) else {
            return Err(Failure::NoMir {
                callee: function,
                function: self.frame().function,
                span,
            });
        };
        let result = match (symbol.as_str(), arguments) {
            ("peeper_rt_v1_alloc" | "malloc", [size]) => {
                let size = self.size_argument(size);
                Some(self.allocated(size, span)?)
            }
            ("calloc", [count, size]) => {
                let (count, size) = (self.size_argument(count), self.size_argument(size));
                let block = match count.checked_mul(size) {
                    Some(bytes) => {
                        let block = self.allocated(bytes, span)?;
                        if let Register::Address(address @ 1..) = block
                            && let Some(bytes) = self.memory.bytes_mut(address, bytes)
                        {
                            bytes.fill(0);
                        }
                        block
                    }
                    None => Register::Address(0),
                };
                Some(block)
            }
            ("peeper_rt_v1_free" | "free", [block]) => {
                let address = self.address_argument(block);
                if address != 0 && self.memory.free(address).is_none() {
                    return Err(self.fail("this frees memory that is not allocated", span));
                }
                None
            }
            ("peeper_rt_v1_write", [fd, buffer, length]) => {
                let (fd, address) = (self.i32_argument(fd), self.address_argument(buffer));
                let length = self.size_argument(length);
                self.check_output(length, span)?;
                let bytes = self.read_bytes(address, length, span)?.to_vec();
                let written = self.output(fd, &bytes, span)?;
                Some(self.integer_result(id, if written { length } else { u64::MAX }))
            }
            // No program here has an input.
            ("peeper_rt_v1_read", [_, _, _]) => Some(self.integer_result(id, 0)),
            ("peeper_rt_v1_exit", [code]) => return Err(Failure::Exited(self.i32_argument(code))),
            (symbol, _) => {
                let message = format!("`{symbol}` is outside Peeper, so it cannot run here");
                return Err(self.fail(message, span));
            }
        };
        self.set_value(id, result)
    }

    /// `value`, wrapped to the integer type the call `id` gives.
    fn integer_result(&self, id: Id<mir::Instruction>, value: u64) -> Register {
        let ty = self.frame().body.instructions[id].ty;
        let Type::Integer(integer) = self.types[ty] else {
            unreachable!("this call gives an integer")
        };
        Register::Scalar(Value::Integer(Limbs::new(integer.bits, &[value])))
    }
}
