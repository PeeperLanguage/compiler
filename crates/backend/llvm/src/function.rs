//! One function's `define`: a walk over its MIR blocks. Each MIR block is an
//! LLVM block (`bb3`); an operation that can trap at run time checks first
//! and continues in a new block (`bb3.1`), so a `phi` names the block where
//! its incoming MIR block ends.

mod arithmetic;
mod arrays;
mod calls;
mod debug_info;
mod memory;
mod print;

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;
use std::iter::Peekable;

use arena::Id;
use model::DefId;
use model::constant::Value;
use model::mir;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Terminator;
use model::operators::BinaryOp;
use model::operators::UnaryOp;
use model::semantic_types::Float;
use model::semantic_types::Integer;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use model::semantic_types::range_field;
use rustc_hash::FxHashMap;

use crate::Module;
use crate::function::debug_info::DebugLocation;
use crate::text::constant_text;
use crate::text::quoted;
use crate::text::symbol;
use crate::text::type_text;
use model::layout::in_memory;

/// The runtime functions `print` calls, with their LLVM parameters.
const PRINT_TEXT: (&str, &str) = ("peeper_rt_v1_print_text", "ptr");
const PRINT_INT: (&str, &str) = ("peeper_rt_v2_print_int", "i32, i32, ptr");
const PRINT_FLOAT: (&str, &str) = ("peeper_rt_v1_print_float", "double, i32");
const PRINT_POINTER: (&str, &str) = ("peeper_rt_v1_print_pointer", "ptr");
const FLUSH: (&str, &str) = ("peeper_rt_v1_flush", "");
/// The intrinsic that copies a value kept in memory (`in_memory`).
const MEMCPY: &str = "llvm.memcpy.p0.p0.i64";

impl Module<'_> {
    /// Adds the function `definition` of this module, whose MIR is `body`.
    pub(crate) fn add_function(&mut self, definition: DefId, body: &mir::Body, types: &Types) {
        let program = self.program;
        let symbol = symbol(program, definition);
        let (subprogram, scope_metadata) = self
            .add_debug_info(definition, &symbol, body, types)
            .unzip();
        // Each string constant's global.
        let strings = body
            .constants
            .iter()
            .map(|constant| match &constant.value {
                Value::Bytes(bytes) => Some(self.add_string(bytes)),
                _ => None,
            })
            .collect();
        let mut function = Function {
            module: self,
            strings,
            body,
            types,
            debug: DebugNodes {
                scopes: scope_metadata.unwrap_or_default(),
                locations: FxHashMap::default(),
            },
            panics: String::new(),
            checks: body
                .blocks
                .ids()
                .map(|block| {
                    let block = &body.blocks[block];
                    let instructions = body.lists.items(block.instructions);
                    instructions.map(|id| traps(body, types, id)).sum()
                })
                .collect(),
            current: CurrentBlock::default(),
            memory: String::new(),
        };
        // Written straight onto the module's functions, taken out while
        // `function` borrows the module.
        let mut out = std::mem::take(&mut function.module.functions);
        // A parameter kept in memory is the caller's private copy
        // (`write_call`), so it is the variable's memory.
        let arguments = (0..body.parameters).map(|index| {
            let ty = body.variables[Id::from_index(index)].ty;
            fmt::from_fn(move |out| {
                if in_memory(types, ty) {
                    write!(out, "ptr %v{index}")
                } else {
                    write!(out, "{} %arg{index}", type_text(types, ty))
                }
            })
        });
        // A result kept in memory is written where a first parameter
        // points, as `write_call` passes it.
        if in_memory(types, body.result) {
            let separator = if body.parameters > 0 { ", " } else { "" };
            _ = write!(
                out,
                "define void {}(ptr %result{separator}",
                global(&symbol)
            );
        } else {
            let result = type_text(types, body.result);
            _ = write!(out, "define {result} {}(", global(&symbol));
        }
        _ = write!(out, "{})", comma_separated(arguments));
        if let Some(subprogram) = subprogram {
            _ = write!(out, " !dbg !{subprogram}");
        }
        out.push_str(" {\nentry:\n");
        function.variables(&mut out);
        let entry_end = out.len();
        out.push_str("  br label %bb0\n");
        for block in body.blocks.ids() {
            function.write_block(&mut out, block);
        }
        let Function { panics, memory, .. } = function;
        // Most functions keep nothing in memory; inserting nothing would
        // still move the whole function.
        if !memory.is_empty() {
            out.insert_str(entry_end, &memory);
        }
        out.push_str(&panics);
        out.push_str("}\n\n");
        self.functions = out;
        self.written.functions.insert(symbol);
    }
}

/// How many times the instruction `id` can trap at run time, so it checks
/// first: a division or shift of integers by a value not known at compile
/// time, each element of its place at an index not known then, and a
/// `Check`, and array parts whose counts are not all known then. Check
/// rejects a constant that would trap.
// Called for every instruction; the array parts arm made the inliner drop it,
// costing a compile about 10 M instructions.
#[inline(always)]
fn traps(body: &mir::Body, types: &Types, id: Id<mir::Instruction>) -> usize {
    match body.instructions[id].kind {
        InstructionKind::Binary { op, left, right } => {
            let divides_or_shifts = matches!(
                op,
                BinaryOp::Divide | BinaryOp::Remainder | BinaryOp::ShiftLeft | BinaryOp::ShiftRight
            );
            usize::from(
                divides_or_shifts
                    && !matches!(right, Operand::Constant(_))
                    && matches!(types[body.operand_type(left)], Type::Integer(_)),
            )
        }
        InstructionKind::Load(ref place)
        | InstructionKind::Move(ref place)
        | InstructionKind::Store { ref place, .. }
        | InstructionKind::Borrow { ref place, .. }
        | InstructionKind::Address(ref place) => {
            let unknown = (place.projections.iter())
                .filter(|step| {
                    matches!(step, mir::Projection::Index(index) if !matches!(index, Operand::Constant(_)))
                })
                .count();
            // A slice is only ever all of a variable, so its elements are the
            // first step; an element there is checked even at a constant
            // index, as no length is known at compile time.
            let in_slice =
                matches!(
                    place.projections.get(1),
                    Some(mir::Projection::Index(Operand::Constant(_)))
                ) && matches!(types[body.variables[place.variable].ty], Type::Slice { .. });
            unknown + usize::from(in_slice)
        }
        InstructionKind::Check { .. } => 1,
        // Any part but an element or an array is counted when the program
        // runs, as is a range unless it is written here with constant
        // `left`, `step` and `inclusive`; a new kind of part is checked
        // until it is listed here.
        InstructionKind::ArrayParts(ref parts) => {
            let Type::Array { element, .. } = types[body.instructions[id].ty] else {
                unreachable!("array parts make an array")
            };
            usize::from(parts.iter().any(|&part| {
                let ty = body.operand_type(part);
                match types[ty] {
                    _ if types.same(ty, element) => false,
                    Type::Array { .. } => false,
                    Type::Range { .. } => {
                        [range_field::LEFT, range_field::STEP, range_field::INCLUSIVE]
                            .into_iter()
                            .any(|field| {
                                !matches!(
                                    written_range_field(body, part, field),
                                    Some(Operand::Constant(_))
                                )
                            })
                    }
                    _ => true,
                }
            }))
        }
        _ => 0,
    }
}

/// Whether the instruction `id` spreads a slice into an array. The slice
/// may view the memory the array would be built in, a stored place
/// (`a = [4]i32{0, s...}` of `s = &a[0..3]`) or `%result` (`a = f(&a)`), so
/// that array is built in its own memory and then copied (`value_address`).
fn spreads_slice(body: &mir::Body, types: &Types, id: Id<mir::Instruction>) -> bool {
    let InstructionKind::ArrayParts(ref parts) = body.instructions[id].kind else {
        return false;
    };
    (parts.iter()).any(|&part| matches!(types[body.operand_type(part)], Type::Slice { .. }))
}

/// Field `field` of the range value `range` (`range_fields`) as the
/// `Aggregate` that makes it in `body` gives it: a range written where it
/// is spread; `None` for a range value from elsewhere.
// shortcut: the `Aggregate` still writes the range to memory, which nothing
// then reads (an alloca and a store per field in a debug build); skip
// writing an `Aggregate` whose only use is `ArrayParts` if debug size matters.
fn written_range_field(body: &mir::Body, range: Operand, field: usize) -> Option<Operand> {
    let Operand::Value(id) = range else {
        return None;
    };
    match body.instructions[id].kind {
        InstructionKind::Aggregate(ref fields) => Some(fields[field]),
        _ => None,
    }
}

/// The LLVM name of the value the instruction `id` gives, which the checks
/// it writes first extend (`%i3.z`).
fn value_name(id: Id<mir::Instruction>) -> impl std::fmt::Display {
    std::fmt::from_fn(move |out| write!(out, "%i{}", id.index()))
}

/// `items` with a comma between each two, written where it is printed.
fn comma_separated<T: Display>(items: impl Iterator<Item = T> + Clone) -> impl Display {
    fmt::from_fn(move |out| {
        for (index, item) in items.clone().enumerate() {
            if index > 0 {
                out.write_str(", ")?;
            }
            item.fmt(out)?;
        }
        Ok(())
    })
}

/// `symbol` as an LLVM global name.
pub(crate) fn global(symbol: &str) -> String {
    format!("@{}", quoted(symbol.as_bytes()))
}

/// Writing one function's `define`, into the `out` its writing methods are
/// given.
struct Function<'m, 'p, 'b> {
    module: &'m mut Module<'p>,
    strings: Vec<Option<String>>, // by constant: the global of each string
    body: &'b mir::Body,
    types: &'b Types,
    debug: DebugNodes,
    panics: String,     // the blocks that call the panic function, after the others
    checks: Vec<usize>, // how many checks each block writes (`traps`)
    current: CurrentBlock,
    /// The `alloca` of each instruction's value kept in memory (`in_memory`),
    /// named as the value, for the entry block, so a loop reuses it.
    // shortcut: one slot per such value, so a function copying a big array
    // many times needs that many copies of stack; share slots or move
    // without a copy when a real program hits the stack limit.
    memory: String,
}

/// The function's debug info nodes, by what they describe; empty in a
/// release build.
struct DebugNodes {
    scopes: Vec<usize>, // by MIR debug scope (`Module::add_debug_info`)
    locations: FxHashMap<(usize, u32), usize>, // each scope and start offset's DILocation
}

/// The MIR block being written (`write_block`).
#[derive(Default)]
struct CurrentBlock {
    block: usize,
    checked: usize,                         // its checks so far
    returned: Option<Id<mir::Instruction>>, // its value built in `%result`
}

impl Function<'_, '_, '_> {
    /// The `alloca` of each variable but a parameter kept in memory
    /// (`add_function`) and, in a debug build, the debug declaration of
    /// each one with a name.
    fn variables(&mut self, out: &mut String) {
        let file = self.module.file();
        for variable in self.body.variables.ids() {
            let index = variable.index();
            let mir::Variable {
                ty,
                debug_scope,
                hidden,
                ..
            } = self.body.variables[variable];
            if index >= self.body.parameters || !in_memory(self.types, ty) {
                _ = writeln!(out, "  %v{index} = alloca {}", type_text(self.types, ty));
            }
            let Some(metadata) = &mut self.module.metadata else {
                continue;
            };
            if hidden {
                continue;
            }
            let span = self.body.variables.span(variable);
            let (program, target) = (self.module.program, self.module.target);
            let debug_type = metadata
                .debug_type(program, self.types, ty, target)
                .unwrap_or_default();
            let (line, _) = file.position(span.start as usize);
            let argument = if index < self.body.parameters {
                format!("arg: {}, ", index + 1)
            } else {
                String::new()
            };
            let file_metadata = metadata.file;
            let declared = metadata.add(format_args!(
                "!DILocalVariable(name: {}, {argument}scope: !{}, file: !{file_metadata}, line: {line}, type: !{debug_type})",
                quoted(file.slice(span).as_bytes()),
                self.debug.scopes[debug_scope.index()],
            ));
            let location = self.location(span, debug_scope);
            _ = writeln!(
                out,
                "  call void @llvm.dbg.declare(metadata ptr %v{index}, \
                 metadata !{declared}, metadata !DIExpression()){location}",
            );
        }
    }

    /// The label of the LLVM block where the MIR block `block` ends: after
    /// its last check.
    fn end_label(&self, block: Id<mir::Block>) -> impl Display {
        let checks = self.checks[block.index()];
        fmt::from_fn(move |out| match checks {
            0 => write!(out, "bb{}", block.index()),
            checks => write!(out, "bb{}.{checks}", block.index()),
        })
    }

    fn operand(&self, operand: Operand) -> impl Display {
        fmt::from_fn(move |out| match operand {
            Operand::Constant(constant) => match &self.strings[constant.index()] {
                Some(global) => out.write_str(global),
                None => constant_text(self.types, &self.body.constants[constant.index()]).fmt(out),
            },
            Operand::Argument(index) => write!(out, "%arg{index}"),
            Operand::Value(instruction) => value_name(instruction).fmt(out),
        })
    }

    /// `operand` with its type before it, as a call argument.
    fn typed(&self, operand: Operand) -> impl Display {
        let ty = self.body.operand_type(operand);
        fmt::from_fn(move |out| {
            write!(
                out,
                "{} {}",
                type_text(self.types, ty),
                self.operand(operand)
            )
        })
    }

    /// Writes the block `block`.
    fn write_block(&mut self, out: &mut String, block: Id<mir::Block>) {
        _ = writeln!(out, "bb{}:", block.index());
        let (body, types) = (self.body, self.types);
        let mir = &body.blocks[block];
        // A value kept in memory that the block returns as soon as it is
        // made is built in `%result` (`value_address`).
        let returned = match mir.terminator {
            Terminator::Return {
                value: Some(Operand::Value(value)),
                ..
            } if in_memory(types, body.result)
                && matches!(
                    body.instructions[value].kind,
                    InstructionKind::Load(_)
                        | InstructionKind::Move(_)
                        | InstructionKind::Aggregate(_)
                        | InstructionKind::ArrayParts(_)
                        | InstructionKind::Call { .. }
                )
                && !spreads_slice(body, types, value)
                && (body.lists.items(mir.instructions))
                    .filter(|&id| body.instructions[id].kind.runs())
                    .last()
                    == Some(value) =>
            {
                Some(value)
            }
            _ => None,
        };
        self.current = CurrentBlock {
            block: block.index(),
            checked: 0,
            returned,
        };
        let mut instructions = body.lists.items(mir.instructions).peekable();
        while let Some(id) = instructions.next() {
            self.write_instruction(out, id, &mut instructions);
        }
        // The line, then the location of its span, if it has one.
        let span = match mir.terminator {
            Terminator::Goto(target) => {
                _ = write!(out, "  br label %bb{}", target.index());
                None
            }
            Terminator::Branch {
                condition,
                span,
                then,
                otherwise,
            } => {
                _ = write!(
                    out,
                    "  br i1 {}, label %bb{}, label %bb{}",
                    self.operand(condition),
                    then.index(),
                    otherwise.index()
                );
                Some(span)
            }
            Terminator::Return {
                value: Some(value),
                span,
            } if in_memory(self.types, self.body.result) => {
                if self.current.returned.is_none() {
                    let location = self.location(span, mir.debug_scope);
                    self.write_store(out, &"%result", value, &location);
                }
                out.push_str("  ret void");
                Some(span)
            }
            Terminator::Return {
                value: Some(value),
                span,
            } => {
                _ = write!(out, "  ret {}", self.typed(value));
                Some(span)
            }
            Terminator::Panic { message, span } => {
                let Value::Bytes(ref text) = body.constants[message.index()].value else {
                    unreachable!("a panic's message is bytes")
                };
                self.write_panic(out, text, span, mir.debug_scope);
                None
            }
            // Reachable with a result only in a function with an error,
            // which is not lowered.
            Terminator::FallOff(_) if self.body.result != TypeId::VOID => {
                out.push_str("  unreachable");
                None
            }
            Terminator::Return { value: None, span } | Terminator::FallOff(span) => {
                out.push_str("  ret void");
                Some(span)
            }
        };
        let location = span.map_or(DebugLocation(None), |span| {
            self.location(span, mir.debug_scope)
        });
        _ = writeln!(out, "{location}");
    }

    /// Writes the instruction `id`, before the `rest` of its block.
    fn write_instruction(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        rest: &mut Peekable<impl Iterator<Item = Id<mir::Instruction>>>,
    ) {
        let mir::Instruction {
            ty,
            ref kind,
            debug_scope,
        } = self.body.instructions[id];
        if !kind.runs() {
            return; // no code, so no location either
        }
        let span = self.body.instructions.span(id);
        let location = self.location(span, debug_scope);
        let name = value_name(id);
        let types = self.types;
        _ = match *kind {
            // A whole variable's value, the most read and written, skips
            // `write_address` (measured: 35 M instructions a compile of the
            // benchmark, with the store's).
            InstructionKind::Load(ref place) | InstructionKind::Move(ref place)
                if !in_memory(types, ty)
                    && let Some(variable) = place.whole_variable() =>
            {
                write!(
                    out,
                    "  {name} = load {}, ptr %v{}",
                    type_text(types, ty),
                    variable.index()
                )
            }
            InstructionKind::Load(ref place) | InstructionKind::Move(ref place) => {
                let address = self.write_address(out, place, &location, id);
                if in_memory(types, ty) {
                    let to = self.value_address(out, id, ty, rest);
                    self.write_copy(out, &to, &address, ty, &location);
                    return;
                }
                write!(
                    out,
                    "  {name} = load {}, ptr {address}",
                    type_text(types, ty)
                )
            }
            InstructionKind::Store { ref place, value }
                if !in_memory(types, self.body.operand_type(value))
                    && let Some(variable) = place.whole_variable() =>
            {
                write!(
                    out,
                    "  store {}, ptr %v{}",
                    self.typed(value),
                    variable.index()
                )
            }
            // The parameter's memory is the caller's copy (`add_function`).
            InstructionKind::Store {
                value: value @ Operand::Argument(_),
                ..
            } if in_memory(types, self.body.operand_type(value)) => return,
            InstructionKind::Store { ref place, value } => {
                let address = self.write_address(out, place, &location, id);
                self.write_store(out, &address, value, &location);
                return;
            }
            InstructionKind::Borrow { ref place, .. } | InstructionKind::Address(ref place) => {
                if !matches!(types[ty], Type::Slice { .. }) {
                    self.write_place_address(out, &name, place, &location, id);
                    return; // the address is the value
                }
                // A slice: the elements' address, then their count.
                let address = format!("{name}.p");
                let viewed = self.write_place_address(out, &address, place, &location, id);
                let length = match (place.projections.last(), &types[viewed]) {
                    (Some(&mir::Projection::Subslice(subslice)), _) => {
                        let count = self.body.subslices[subslice.index()].count;
                        self.operand(count).to_string()
                    }
                    (_, Type::Array { length, .. }) => length.to_string(),
                    (_, Type::Slice { .. }) => format!("{address}.0.len"),
                    _ => unreachable!("a slice is taken of an array or a slice's elements"),
                };
                let bits = self.module.target.pointer_bits;
                _ = writeln!(
                    out,
                    "  {name}.a = insertvalue %slice poison, ptr {address}, 0{location}"
                );
                write!(
                    out,
                    "  {name} = insertvalue %slice {name}.a, i{bits} {length}, 1"
                )
            }
            InstructionKind::Binary { op, left, right } => {
                self.write_binary(out, id, op, left, right);
                Ok(())
            }
            InstructionKind::Unary { op, value } => {
                let text = type_text(types, ty);
                let value = self.operand(value);
                match (op, &types[ty]) {
                    (UnaryOp::Negate, Type::Float(_)) => {
                        write!(out, "  {name} = fneg {text} {value}")
                    }
                    (UnaryOp::Negate, _) => write!(out, "  {name} = sub {text} 0, {value}"),
                    (UnaryOp::LogicalNot, _) => write!(out, "  {name} = xor {text} {value}, true"),
                    (UnaryOp::BitNot, _) => write!(out, "  {name} = xor {text} {value}, -1"),
                }
            }
            InstructionKind::Convert(value) => {
                let from = self.body.operand_type(value);
                // `bool` is LLVM's `i1`: a 1-bit unsigned integer.
                let integer = |ty: TypeId| match types[ty] {
                    Type::Integer(integer) => Some(integer),
                    Type::Bool => Some(Integer {
                        signed: false,
                        bits: 1,
                    }),
                    _ => None,
                };
                let to_text = type_text(types, ty);
                match (integer(from), integer(ty), &types[from]) {
                    (None, Some(to), &Type::Float(float)) => {
                        // Saturating, as check folds it: cut toward zero, the
                        // nearest end of the integer's range outside it, 0 for NaN.
                        let sign = if to.signed { 's' } else { 'u' };
                        let suffix = match float {
                            Float::F32 => "f32",
                            Float::F64 => "f64",
                        };
                        let intrinsic = format!("llvm.fpto{sign}i.sat.{to_text}.{suffix}");
                        let from_text = type_text(types, from);
                        self.module.declare(&intrinsic, || {
                            format!("declare {to_text} @{intrinsic}({from_text})\n\n")
                        });
                        write!(
                            out,
                            "  {name} = call {to_text} @{intrinsic}({})",
                            self.typed(value)
                        )
                    }
                    pair => {
                        let conversion = match pair {
                            (Some(from), Some(to), _) if to.bits > from.bits && from.signed => {
                                "sext"
                            }
                            (Some(from), Some(to), _) if to.bits > from.bits => "zext",
                            (Some(from), Some(to), _) if to.bits < from.bits => "trunc",
                            // The same bits, read with the other sign.
                            (Some(_), Some(_), _) => "bitcast",
                            (Some(from), None, _) if from.signed => "sitofp",
                            (Some(_), None, _) => "uitofp",
                            // A pointer cast keeps the address.
                            (None, None, Type::RawPointer { .. }) => "bitcast",
                            (None, None, Type::Float(Float::F32)) => "fpext",
                            (None, None, _) => "fptrunc",
                            (None, Some(_), _) => {
                                unreachable!("check converts only numbers, `bool` and pointers")
                            }
                        };
                        write!(
                            out,
                            "  {name} = {conversion} {} to {to_text}",
                            self.typed(value)
                        )
                    }
                }
            }
            InstructionKind::Call {
                function,
                ref arguments,
            } => {
                self.write_call(out, id, function, arguments, rest, &location);
                Ok(())
            }
            InstructionKind::Print {
                newline,
                ref values,
            } => {
                self.write_print(out, id, newline, values, &location);
                return; // each line has its location
            }
            InstructionKind::ArrayParts(ref parts) => {
                self.write_array_parts(out, id, parts, rest, &location);
                return;
            }
            // Each part written where it goes in the value's memory.
            InstructionKind::Aggregate(ref parts) => {
                let memory = self.value_address(out, id, ty, rest);
                let text = type_text(types, ty);
                let index_type = match types[ty] {
                    Type::Array { .. } => "i64",
                    _ => "i32",
                };
                for (index, &part) in parts.iter().enumerate() {
                    let address = format!("{name}.{index}");
                    _ = writeln!(
                        out,
                        "  {address} = getelementptr inbounds {text}, ptr {memory}, i32 0, \
                         {index_type} {index}{location}"
                    );
                    self.write_store(out, &address, part, &location);
                }
                return;
            }
            // A slice's length is in its value. Another field is in the memory
            // of the value it is part of, which no one changes: its address
            // when it is kept in memory, else loaded from there.
            InstructionKind::Field { value, ref fields } => {
                let value_type = self.body.operand_type(value);
                if let Type::Slice { .. } = types[value_type] {
                    let value = self.operand(value);
                    write!(out, "  {name} = extractvalue %slice {value}, {}", fields[0])
                } else {
                    let field_address = fmt::from_fn(|out| {
                        if in_memory(types, ty) {
                            write!(out, "{name}")
                        } else {
                            write!(out, "{name}.f")
                        }
                    });
                    _ = write!(
                        out,
                        "  {field_address} = getelementptr inbounds {}, ptr {}, i32 0",
                        type_text(types, value_type),
                        self.operand(value)
                    );
                    for index in fields {
                        _ = write!(out, ", i32 {index}");
                    }
                    if in_memory(types, ty) {
                        Ok(())
                    } else {
                        _ = writeln!(out, "{location}");
                        write!(
                            out,
                            "  {name} = load {}, ptr {field_address}",
                            type_text(types, ty)
                        )
                    }
                }
            }
            InstructionKind::Phi(ref incoming) => {
                let function = &*self;
                let incoming = incoming.iter().map(|&(block, value)| {
                    let (value, label) = (function.operand(value), function.end_label(block));
                    fmt::from_fn(move |out| write!(out, "[ {value}, %{label} ]"))
                });
                let incoming = comma_separated(incoming);
                _ = writeln!(out, "  {name} = phi {} {incoming}", type_text(types, ty));
                return; // no location: it runs where control arrives
            }
            InstructionKind::Check { failed, message } => {
                let failed = self.operand(failed).to_string();
                self.check(out, &failed, message, id);
                return; // `check` ends the line with the next block's label
            }
            InstructionKind::ScopeEnd(_) => unreachable!("returned above"),
        };
        _ = writeln!(out, "{location}");
    }
}
