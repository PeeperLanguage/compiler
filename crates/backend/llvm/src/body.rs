//! One function's `define`, written from its MIR of core concepts
//! (`model::mir`): each MIR block a jump reaches is an LLVM block
//! (`bb3`), and a `Check` branches to a panic block and goes on in a new
//! one (`bb3.1`).
//! Every slot is bytes on the stack, so no language type is read here: the
//! MIR's classes, sizes and passings are all the writer knows.

use std::fmt;
use std::fmt::Display;
use std::fmt::Write;

use arena::Id;
use model::FunctionCopy;
use model::mir::Block;
use model::mir::Body;
use model::mir::CPassing;
use model::mir::CSignature;
use model::mir::Callee;
use model::mir::Class;
use model::mir::Comparison;
use model::mir::Conversion;
use model::mir::DebugScope;
use model::mir::DebugTypes;
use model::mir::Extension;
use model::mir::FloatOp;
use model::mir::Global;
use model::mir::Instruction;
use model::mir::InstructionKind;
use model::mir::IntegerOp;
use model::mir::Operand;
use model::mir::Passing;
use model::mir::Place;
use model::mir::Root;
use model::mir::Signature;
use model::mir::Slot;
use model::mir::Step;
use model::mir::Terminator;
use runtime_abi::abi::PANIC_EXIT_CODE;
use runtime_abi::abi::STDERR;
use runtime_abi::messages::panic_text;
use runtime_abi::symbols::Carrier;
use runtime_abi::symbols::PANIC_EXIT;
use runtime_abi::symbols::PANIC_WRITE;
use rustc_hash::FxHashMap;
use source::SourceFile;
use source::Span;

use crate::Module;
use crate::debug_info::DebugLocation;
use crate::debug_info::debug_position;
use crate::text::quoted;

/// The intrinsic that copies bytes from one place to another.
const MEMCPY: &str = "llvm.memcpy.p0.p0.i64";

/// A function the compile checked, for a worker to write.
pub(crate) struct Defined {
    pub(crate) copy: FunctionCopy,
    /// As a debugger shows it, `const` arguments too; empty in a release
    /// build.
    pub(crate) shown_name: String,
    pub(crate) body: Body,
}

impl Module<'_> {
    /// Adds the function `defined`: one of this module, or a copy of another
    /// module's function this module called first. `symbols` holds the
    /// symbol of it and of each function it names; `debug_types` what its
    /// slots' debug types are, in a debug build.
    pub(crate) fn add_body(
        &mut self,
        defined: &Defined,
        symbols: &FxHashMap<FunctionCopy, String>,
        debug_types: &DebugTypes,
    ) {
        let Defined {
            copy,
            shown_name,
            body,
        } = defined;
        let symbol = &symbols[copy];
        let file = &self.program.modules[copy.definition.module.index()]
            .ast
            .file;
        let globals = (body.globals.iter())
            .map(|global| match global {
                Global::Bytes(bytes) => self.add_string(bytes),
                Global::Functions(entries) => self.add_table(entries, symbols),
            })
            .collect();
        let (subprogram, scopes) = self
            .add_debug_info(*copy, file, symbol, shown_name, body, debug_types)
            .unzip();
        let targets = jump_targets(body);
        let mut writer = Writer {
            module: self,
            body,
            file,
            symbols,
            globals,
            scopes: scopes.unwrap_or_default(),
            locations: FxHashMap::default(),
            panics: String::new(),
            memory: String::new(),
            block: 0,
            checked: 0,
            reached: reached_blocks(body, &targets),
            targets,
            read: values_read(body),
        };
        // Written straight onto the module's functions, taken out while
        // `writer` borrows the module.
        let mut out = std::mem::take(&mut writer.module.functions);
        let (result, parameters) = peeper_types(&body.signature);
        // A result kept in memory is written where the hidden first
        // parameter points.
        let hidden = body.signature.hidden_parameters() as usize;
        let parameters = (parameters.iter().enumerate()).map(|(index, ty)| {
            fmt::from_fn(move |out| match index.checked_sub(hidden) {
                Some(written) => write!(out, "{ty} %a{written}"),
                None => write!(out, "{ty} %result"),
            })
        });
        _ = write!(
            out,
            "define {result} {}({})",
            global(symbol),
            comma_separated(parameters)
        );
        if let Some(subprogram) = subprogram {
            _ = write!(out, " !dbg !{subprogram}");
        }
        out.push_str(" {\nentry:\n");
        writer.write_slots(&mut out, debug_types);
        let entry_end = out.len();
        _ = writeln!(out, "  br label %bb{}", writer.targets[0].index());
        for block in body.blocks.ids() {
            if writer.reached[block.index()] {
                writer.write_block(&mut out, block);
            }
        }
        let Writer { panics, memory, .. } = writer;
        // Most functions call no C function that takes memory; inserting
        // nothing would still move the whole function.
        if !memory.is_empty() {
            out.insert_str(entry_end, &memory);
        }
        out.push_str(&panics);
        out.push_str("}\n\n");
        self.functions = out;
        self.written.functions.insert(symbol.clone());
    }

    /// The global holding the table of functions `entries`, written once
    /// per module, with a `declare` of each.
    fn add_table(
        &mut self,
        entries: &[(FunctionCopy, Signature)],
        symbols: &FxHashMap<FunctionCopy, String>,
    ) -> String {
        let mut constant = format!("[{} x ptr] [", entries.len());
        for (index, (copy, signature)) in entries.iter().enumerate() {
            let symbol = &symbols[copy];
            self.declare(symbol, || declaration(symbol, signature));
            let separator = if index > 0 { ", " } else { "" };
            _ = write!(constant, "{separator}ptr {}", global(symbol));
        }
        constant.push(']');
        let count = self.written.tables.len();
        let id = *self
            .written
            .tables
            .entry(constant)
            .or_insert_with_key(|constant| {
                _ = writeln!(
                    self.strings,
                    "@.table.{count} = private unnamed_addr constant {constant}"
                );
                count
            });
        format!("@.table.{id}")
    }
}

/// The LLVM type of a register of `class`.
pub(crate) fn llvm_type(class: Class) -> impl Display {
    fmt::from_fn(move |out| match class {
        Class::F32 => out.write_str("float"),
        Class::F64 => out.write_str("double"),
        // `i32`, `i200`, `ptr`, as the MIR's dump names them.
        _ => class.fmt(out),
    })
}

/// The instruction an `Integer` of `operation` is; a signed division by
/// what may be -1 is `signed_division`'s lines.
pub fn integer_instruction(operation: IntegerOp) -> &'static str {
    match operation {
        IntegerOp::Add => "add",
        IntegerOp::Subtract => "sub",
        IntegerOp::Multiply => "mul",
        IntegerOp::SignedDivide => "sdiv",
        IntegerOp::UnsignedDivide => "udiv",
        IntegerOp::SignedRemainder => "srem",
        IntegerOp::UnsignedRemainder => "urem",
        IntegerOp::ShiftLeft => "shl",
        IntegerOp::SignedShiftRight => "ashr",
        IntegerOp::UnsignedShiftRight => "lshr",
        IntegerOp::And => "and",
        IntegerOp::Or => "or",
        IntegerOp::Xor => "xor",
    }
}

/// The instruction a `Float` of `operation` is.
pub fn float_instruction(operation: FloatOp) -> &'static str {
    match operation {
        FloatOp::Add => "fadd",
        FloatOp::Subtract => "fsub",
        FloatOp::Multiply => "fmul",
        FloatOp::Divide => "fdiv",
        FloatOp::Remainder => "frem",
    }
}

/// The `icmp` or `fcmp` with its condition that compares two registers of
/// `class` as `comparison`.
pub fn comparison_instruction(comparison: Comparison, class: Class) -> &'static str {
    let float = matches!(class, Class::F32 | Class::F64);
    match comparison {
        Comparison::Equal if float => "fcmp oeq",
        Comparison::NotEqual if float => "fcmp une",
        Comparison::Equal => "icmp eq",
        Comparison::NotEqual => "icmp ne",
        Comparison::SignedLess => "icmp slt",
        Comparison::SignedLessOrEqual => "icmp sle",
        Comparison::UnsignedLess => "icmp ult",
        Comparison::UnsignedLessOrEqual => "icmp ule",
        Comparison::FloatLess => "fcmp olt",
        Comparison::FloatLessOrEqual => "fcmp ole",
    }
}

/// The cast instruction of `conversion`; a float to an integer is the
/// saturating intrinsic `saturating_conversion` names instead.
pub fn conversion_instruction(conversion: Conversion) -> &'static str {
    match conversion {
        Conversion::SignExtend => "sext",
        Conversion::ZeroExtend => "zext",
        Conversion::Truncate => "trunc",
        Conversion::SignedToFloat => "sitofp",
        Conversion::UnsignedToFloat => "uitofp",
        Conversion::FloatWiden => "fpext",
        Conversion::FloatNarrow => "fptrunc",
        Conversion::PointerCast => "bitcast",
        Conversion::FloatToSigned { .. } | Conversion::FloatToUnsigned { .. } => {
            unreachable!("a float becomes an integer by `saturating_conversion`")
        }
    }
}

/// The intrinsic that converts a float of `from` to an integer of `bits`
/// bits, toward zero and to the nearest end of its range, for a
/// `FloatTo*` conversion, and whether it is signed.
pub fn saturating_conversion(conversion: Conversion, from: Class) -> (String, bool) {
    let (bits, signed) = match conversion {
        Conversion::FloatToSigned { bits } => (bits, true),
        Conversion::FloatToUnsigned { bits } => (bits, false),
        _ => unreachable!("only a float to an integer saturates"),
    };
    let kind = if signed { "fptosi" } else { "fptoui" };
    let float = if from == Class::F32 { "f32" } else { "f64" };
    (format!("llvm.{kind}.sat.i{bits}.{float}"), signed)
}

/// The lines that give `left / right` or `left % right`, both of the LLVM
/// type `ty`, signed, as `name`, each ended by `location`. The lowest value
/// divided by -1 wraps to itself, which LLVM leaves undefined: it divides
/// by 1 instead and negates, so the remainder is 0 as it should be.
pub fn signed_division(
    name: &str,
    operation: IntegerOp,
    ty: &dyn Display,
    left: &dyn Display,
    right: &dyn Display,
    location: &dyn Display,
) -> String {
    let mut out = format!(
        "  {name}.m = icmp eq {ty} {right}, -1{location}\n  \
         {name}.d = select i1 {name}.m, {ty} 1, {ty} {right}{location}\n"
    );
    if operation == IntegerOp::SignedRemainder {
        _ = writeln!(out, "  {name} = srem {ty} {left}, {name}.d{location}");
        return out;
    }
    _ = writeln!(
        out,
        "  {name}.q = sdiv {ty} {left}, {name}.d{location}\n  \
         {name}.n = sub {ty} 0, {left}{location}\n  \
         {name} = select i1 {name}.m, {ty} {name}.n, {ty} {name}.q{location}"
    );
    out
}

/// `bits` as an LLVM constant of `class` (docs/ir-design.md, "Integer
/// values must match LLVM"): an integer in decimal, read as signed, a float
/// as the hexadecimal of its value as an `f64`, as LLVM reads a `float`
/// too, and the pointer 0 as `null`.
pub fn constant_text(class: Class, bits: [u64; 2]) -> impl Display {
    fmt::from_fn(move |out| match class {
        Class::I128 => write!(
            out,
            "{}",
            (u128::from(bits[1]) << 64 | u128::from(bits[0])) as i128
        ),
        Class::I8 | Class::I16 | Class::I32 | Class::I64 => {
            let unused = 64 - class.bits();
            write!(out, "{}", ((bits[0] << unused) as i64) >> unused)
        }
        Class::F32 => write!(out, "0x{:016X}", widened(bits[0] as u32)),
        Class::F64 => write!(out, "0x{:016X}", bits[0]),
        Class::Pointer => {
            assert_eq!(bits[0], 0, "a pointer constant is null");
            out.write_str("null")
        }
        Class::Wide(_) => unreachable!("a wide constant is read-only data"),
    })
}

/// The bits of the `f32` of bits `bits` as an `f64`: the same value, and a
/// NaN with the same payload, which a conversion may quiet.
fn widened(bits: u32) -> u64 {
    let value = f32::from_bits(bits);
    if !value.is_nan() {
        return f64::from(value).to_bits();
    }
    let sign = u64::from(bits >> 31) << 63;
    sign | 0x7FF << 52 | u64::from(bits & 0x7F_FFFF) << 29
}

/// `symbol` as an LLVM global name.
pub(crate) fn global(symbol: &str) -> String {
    format!("@{}", quoted(symbol.as_bytes()))
}

/// `items` with a comma between each two, written where it is printed.
pub(crate) fn comma_separated<T: Display>(items: impl Iterator<Item = T> + Clone) -> impl Display {
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

/// The `declare` of `symbol`, a function taking and giving values as
/// `signature` says, in Peeper's convention.
fn declaration(symbol: &str, signature: &Signature) -> String {
    let (result, parameters) = peeper_types(signature);
    let parameters = comma_separated(parameters.iter());
    format!("declare {result} {}({parameters})\n\n", global(symbol))
}

/// The LLVM types a function of `signature` gives and takes, in Peeper's
/// convention: a value in memory by its address, a result in memory where
/// a hidden first parameter points.
fn peeper_types(signature: &Signature) -> (String, Vec<String>) {
    let mut parameters = Vec::with_capacity(signature.parameters.len() + 1);
    let result = match signature.result {
        Some(Passing::Register { class, .. }) => llvm_type(class).to_string(),
        Some(Passing::Memory { .. }) => {
            parameters.push("ptr".to_string());
            "void".to_string()
        }
        None => "void".to_string(),
    };
    parameters.extend(signature.parameters.iter().map(|passing| match *passing {
        Passing::Register { class, .. } => llvm_type(class).to_string(),
        Passing::Memory { .. } => "ptr".to_string(),
    }));
    (result, parameters)
}

/// The LLVM type of a register of `class` C widens as `extension` says,
/// with the attribute that tells so: after the type for a parameter
/// (`i8 zeroext`), before it for a `result` (`zeroext i8`).
fn c_register_type(class: Class, extension: Option<Extension>, result: bool) -> String {
    let attribute = match extension {
        Some(Extension::Sign) => "signext",
        Some(Extension::Zero) => "zeroext",
        None => return llvm_type(class).to_string(),
    };
    let ty = llvm_type(class);
    if result {
        format!("{attribute} {ty}")
    } else {
        format!("{ty} {attribute}")
    }
}

/// Writing one function's `define`, into the `out` its methods are given.
struct Writer<'m, 'p, 'b> {
    module: &'m mut Module<'p>,
    body: &'b Body,
    file: &'p SourceFile, // the function's own, which a copy's module may not be
    symbols: &'b FxHashMap<FunctionCopy, String>,
    globals: Vec<String>,                      // each global's address
    scopes: Vec<usize>, // by debug scope, its metadata; none in a release build
    locations: FxHashMap<(usize, u32), usize>, // each scope and offset's DILocation
    panics: String,     // the blocks that call the panic function, after the others
    /// The `alloca` of the memory a C call passes a value in, for the entry
    /// block, so a loop reuses it.
    memory: String,
    block: usize,            // the MIR block being written
    checked: usize,          // its checks so far, each of which starts a block
    targets: Vec<Id<Block>>, // where a jump to each block goes (`jump_targets`)
    reached: Vec<bool>,      // by block, whether a written jump reaches it
    /// By instruction, whether its value is read as a byte, not only as a
    /// truth; a comparison is widened to a byte only then.
    read: Vec<bool>,
}

impl Writer<'_, '_, '_> {
    /// The `alloca` of each slot and, in a debug build, the debug
    /// declaration of each one a debugger shows.
    fn write_slots(&mut self, out: &mut String, debug_types: &DebugTypes) {
        let body = self.body;
        for slot in body.slots.ids() {
            let index = slot.index();
            let data = &body.slots[slot];
            if body.parameter_in_memory(slot).is_none() {
                _ = writeln!(
                    out,
                    "  %s{index} = alloca [{} x i8], align {}",
                    data.size, data.align
                );
            }
            let (Some(debug_type), Some(metadata)) = (data.debug_type, &mut self.module.metadata)
            else {
                continue;
            };
            let pointer_bits = self.module.target.pointer_bits;
            let debug_type = metadata.debug_type(debug_types, debug_type, pointer_bits);
            let file_metadata = metadata.file_node(&self.file.path);
            let span = body.slots.span(slot);
            let (line, _) = self.file.position(span.start as usize);
            let argument = if index < body.signature.parameters.len() {
                format!("arg: {}, ", index + 1)
            } else {
                String::new()
            };
            let declared = metadata.add(format_args!(
                "!DILocalVariable(name: {}, {argument}scope: !{}, file: !{file_metadata}, line: {line}, type: !{debug_type})",
                quoted(self.file.slice(span).as_bytes()),
                self.scopes[data.debug_scope.index()],
            ));
            let location = self.location(span, data.debug_scope);
            _ = writeln!(
                out,
                "  call void @llvm.dbg.declare(metadata ptr {}, \
                 metadata !{declared}, metadata !DIExpression()){location}",
                slot_name(body, slot),
            );
        }
    }

    /// The DILocation of where `span` starts, in the debug scope
    /// `debug_scope`, as the end of a line; nothing in a release build.
    fn location(&mut self, span: Span, debug_scope: Id<DebugScope>) -> DebugLocation {
        let Some(metadata) = &mut self.module.metadata else {
            return DebugLocation(None);
        };
        let scope = self.scopes[debug_scope.index()];
        let file = self.file;
        // An offset has one line and column, so it is the key: they are
        // found only for a new node.
        let id = *self
            .locations
            .entry((scope, span.start))
            .or_insert_with(|| {
                let (line, column) = debug_position(file, span.start as usize);
                metadata.add(format_args!(
                    "!DILocation(line: {line}, column: {column}, scope: !{scope})"
                ))
            });
        DebugLocation(Some(id))
    }

    fn operand(&self, operand: Operand) -> impl Display {
        fmt::from_fn(move |out| self.write_operand(out, operand))
    }

    /// Writes `operand`. The address of a place no step moves from is its
    /// root's, which no instruction computes (`write_place`).
    fn write_operand(&self, out: &mut fmt::Formatter, operand: Operand) -> fmt::Result {
        match operand {
            Operand::Value(id) => match &self.body.instructions[id].kind {
                InstructionKind::Address { place, .. } | InstructionKind::Borrow { place, .. }
                    if place
                        .steps
                        .iter()
                        .all(|&step| folded(step) == Step::Offset(0)) =>
                {
                    match place.root {
                        Root::Slot(slot) => out.write_str(&slot_name(self.body, slot)),
                        Root::At(at) => self.write_operand(out, at),
                        Root::Result => out.write_str("%result"),
                    }
                }
                _ => write!(out, "%i{}", id.index()),
            },
            Operand::Constant { class, bits } => constant_text(class, bits).fmt(out),
            Operand::Parameter(index) => write!(out, "%a{index}"),
            Operand::Global(index) => out.write_str(&self.globals[index as usize]),
        }
    }

    /// The LLVM type of `operand`'s register.
    fn type_of(&self, operand: Operand) -> impl Display + use<> {
        llvm_type(
            self.body
                .class_of(operand)
                .expect("an operand is a register"),
        )
    }

    /// `operand` with its type before it, as an argument.
    fn typed(&self, operand: Operand) -> impl Display {
        let ty = self.type_of(operand);
        fmt::from_fn(move |out| write!(out, "{ty} {}", self.operand(operand)))
    }

    /// The `i1` that is true when the `I8` `condition` is not 0: a
    /// comparison's own, else one written as `name`.
    fn write_truth(
        &self,
        out: &mut String,
        condition: Operand,
        name: &str,
        location: &DebugLocation,
    ) -> String {
        if let Operand::Value(id) = condition
            && let InstructionKind::Compare { .. } = self.body.instructions[id].kind
        {
            return format!("%i{}.c", id.index());
        }
        _ = writeln!(
            out,
            "  {name} = icmp ne i8 {}, 0{location}",
            self.operand(condition)
        );
        name.to_string()
    }

    /// Writes the instructions that find `place`'s address and gives it:
    /// its root's when no step moves from there, else `name`, with the
    /// steps before the last named from it (`name.1`).
    fn write_place(
        &mut self,
        out: &mut String,
        place: &Place,
        name: &str,
        location: &DebugLocation,
    ) -> String {
        let mut at = match place.root {
            Root::Slot(slot) => slot_name(self.body, slot),
            Root::At(operand) => self.operand(operand).to_string(),
            Root::Result => "%result".to_string(),
        };
        // Offsets in a row, with constant indexes, add up to one.
        let mut steps: Vec<Step> = Vec::with_capacity(place.steps.len());
        for &step in &place.steps {
            let step = folded(step);
            match (step, steps.last_mut()) {
                (Step::Offset(0), _) => {}
                (Step::Offset(bytes), Some(Step::Offset(sum))) => *sum += bytes,
                _ => steps.push(step),
            }
        }
        for (position, &step) in steps.iter().enumerate() {
            let to = if position + 1 == steps.len() {
                name.to_string()
            } else {
                format!("{name}.{}", position + 1)
            };
            match step {
                Step::Offset(bytes) => {
                    _ = writeln!(
                        out,
                        "  {to} = getelementptr inbounds i8, ptr {at}, i64 {bytes}{location}"
                    );
                }
                Step::Through => _ = writeln!(out, "  {to} = load ptr, ptr {at}{location}"),
                // Lowering checked the index is inside.
                Step::Index { index, size, .. } => {
                    _ = writeln!(
                        out,
                        "  {to} = getelementptr inbounds [{size} x i8], ptr {at}, {}{location}",
                        self.typed(index)
                    );
                }
            }
            at = to;
        }
        at
    }

    /// Writes the block `block`.
    fn write_block(&mut self, out: &mut String, block: Id<Block>) {
        _ = writeln!(out, "bb{}:", block.index());
        let body = self.body;
        let data = &body.blocks[block];
        (self.block, self.checked) = (block.index(), 0);
        for id in body.lists.items(data.instructions) {
            self.write_instruction(out, id);
        }
        // The line, then the location of its span, if it has one.
        let span = match data.terminator {
            Terminator::Jump(target) => {
                let target = self.targets[target.index()];
                _ = write!(out, "  br label %bb{}", target.index());
                None
            }
            Terminator::Branch {
                condition,
                span,
                then,
                otherwise,
            } => {
                let location = self.location(span, data.debug_scope);
                let name = format!("%bb{}.c", block.index());
                let condition = self.write_truth(out, condition, &name, &location);
                _ = write!(
                    out,
                    "  br i1 {condition}, label %bb{}, label %bb{}",
                    self.targets[then.index()].index(),
                    self.targets[otherwise.index()].index()
                );
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
                let Global::Bytes(text) = &body.globals[message as usize] else {
                    unreachable!("a panic's message is bytes")
                };
                self.write_panic(out, text, span, data.debug_scope);
                None
            }
            // Reachable with a result only in a function with an error,
            // which is not lowered.
            Terminator::FallOff(_) if body.signature.result.is_some() => {
                out.push_str("  unreachable");
                None
            }
            Terminator::Return { value: None, span } | Terminator::FallOff(span) => {
                out.push_str("  ret void");
                Some(span)
            }
        };
        let location = span.map_or(DebugLocation(None), |span| {
            self.location(span, data.debug_scope)
        });
        _ = writeln!(out, "{location}");
    }

    /// Writes the instruction `id`.
    fn write_instruction(&mut self, out: &mut String, id: Id<Instruction>) {
        let body = self.body;
        let Instruction { kind, debug_scope } = &body.instructions[id];
        if !kind.runs() {
            return; // for the path checks only
        }
        let location = self.location(body.instructions.span(id), *debug_scope);
        let name = format!("%i{}", id.index());
        match kind {
            InstructionKind::Integer {
                operation: IntegerOp::Add,
                class: Class::Pointer,
                left,
                right,
            } => {
                // An address may point anywhere, so not `inbounds`.
                _ = writeln!(
                    out,
                    "  {name} = getelementptr i8, ptr {}, {}{location}",
                    self.operand(*left),
                    self.typed(*right)
                );
            }
            &InstructionKind::Integer {
                operation: operation @ (IntegerOp::SignedDivide | IntegerOp::SignedRemainder),
                class,
                left,
                right,
            } if !self.divides_by_other_than_minus_one(right, class) => {
                let (left, right) = (self.operand(left), self.operand(right));
                out.push_str(&signed_division(
                    &name,
                    operation,
                    &llvm_type(class),
                    &left,
                    &right,
                    &location,
                ));
            }
            InstructionKind::Integer {
                operation,
                class,
                left,
                right,
            } => {
                _ = writeln!(
                    out,
                    "  {name} = {} {} {}, {}{location}",
                    integer_instruction(*operation),
                    llvm_type(*class),
                    self.operand(*left),
                    self.operand(*right)
                );
            }
            InstructionKind::Float {
                operation,
                class,
                left,
                right,
            } => {
                _ = writeln!(
                    out,
                    "  {name} = {} {} {}, {}{location}",
                    float_instruction(*operation),
                    llvm_type(*class),
                    self.operand(*left),
                    self.operand(*right)
                );
            }
            InstructionKind::Compare {
                comparison,
                class,
                left,
                right,
            } => {
                _ = writeln!(
                    out,
                    "  {name}.c = {} {} {}, {}{location}",
                    comparison_instruction(*comparison, *class),
                    llvm_type(*class),
                    self.operand(*left),
                    self.operand(*right)
                );
                if self.read[id.index()] {
                    _ = writeln!(out, "  {name} = zext i1 {name}.c to i8{location}");
                }
            }
            &InstructionKind::Convert {
                conversion:
                    conversion @ (Conversion::FloatToSigned { bits }
                    | Conversion::FloatToUnsigned { bits }),
                from,
                to,
                value,
            } => {
                let (intrinsic, signed) = saturating_conversion(conversion, from);
                let (from_text, saturated) = (llvm_type(from), format!("i{bits}"));
                self.module.declare(&intrinsic, || {
                    format!("declare {saturated} @{intrinsic}({from_text})\n\n")
                });
                let called = if bits == to.bits() {
                    name.clone()
                } else {
                    format!("{name}.s")
                };
                _ = writeln!(
                    out,
                    "  {called} = call {saturated} @{intrinsic}({}){location}",
                    self.typed(value)
                );
                if called != name {
                    let extension = if signed { "sext" } else { "zext" };
                    _ = writeln!(
                        out,
                        "  {name} = {extension} {saturated} {called} to {}{location}",
                        llvm_type(to)
                    );
                }
            }
            InstructionKind::Convert {
                conversion,
                from,
                to,
                value,
            } => {
                _ = writeln!(
                    out,
                    "  {name} = {} {} {} to {}{location}",
                    conversion_instruction(*conversion),
                    llvm_type(*from),
                    self.operand(*value),
                    llvm_type(*to)
                );
            }
            InstructionKind::Load { class, place } => {
                let address = self.write_place(out, place, &format!("{name}.a"), &location);
                // Read-only data is bytes, aligned to none.
                let align = match place.root {
                    Root::At(Operand::Global(_)) => ", align 1",
                    _ => "",
                };
                _ = writeln!(
                    out,
                    "  {name} = load {}, ptr {address}{align}{location}",
                    llvm_type(*class)
                );
            }
            InstructionKind::Store {
                class,
                place,
                value,
            } => {
                let address = self.write_place(out, place, &name, &location);
                _ = writeln!(
                    out,
                    "  store {} {}, ptr {address}{location}",
                    llvm_type(*class),
                    self.operand(*value)
                );
            }
            InstructionKind::Copy { to, from, size } => {
                let to = self.write_place(out, to, &format!("{name}.t"), &location);
                let from = self.write_place(out, from, &format!("{name}.f"), &location);
                self.module.declare(MEMCPY, || {
                    format!("declare void @{MEMCPY}(ptr, ptr, i64, i1)\n\n")
                });
                _ = writeln!(
                    out,
                    "  call void @{MEMCPY}(ptr {to}, ptr {from}, i64 {size}, i1 false){location}"
                );
            }
            // With no step, the address is the root's (`write_operand`).
            InstructionKind::Address { place, .. } | InstructionKind::Borrow { place, .. } => {
                self.write_place(out, place, &name, &location);
            }
            InstructionKind::Call {
                callee,
                signature,
                arguments,
            } => {
                let signature = &body.signatures[*signature as usize];
                match (&signature.c, callee) {
                    (Some(c), &Callee::Function(function)) => {
                        let symbol = &self.symbols[&function];
                        self.write_c_call(out, &name, symbol, signature, c, arguments, &location);
                    }
                    _ => self.write_call(out, &name, *callee, signature, arguments, &location),
                }
            }
            InstructionKind::Check { failed, message } => {
                self.checked += 1;
                let next = format!("bb{}.{}", self.block, self.checked);
                let panic = format!("{next}.panic");
                let failed = self.write_truth(out, *failed, &format!("{name}.c"), &location);
                _ = writeln!(
                    out,
                    "  br i1 {failed}, label %{panic}, label %{next}{location}\n{next}:"
                );
                let mut panics = std::mem::take(&mut self.panics);
                _ = writeln!(panics, "{panic}:");
                let span = body.instructions.span(id);
                self.write_panic(&mut panics, message.as_bytes(), span, *debug_scope);
                panics.push('\n');
                self.panics = panics;
            }
            InstructionKind::Moved { .. } | InstructionKind::Dead(_) => {
                unreachable!("returned above")
            }
        }
    }

    /// Whether `divisor`, of `class`, is a constant other than -1, by which
    /// a signed division cannot overflow.
    fn divides_by_other_than_minus_one(&self, divisor: Operand, class: Class) -> bool {
        let Operand::Constant { bits, .. } = divisor else {
            return false;
        };
        let all = u128::MAX >> (128 - class.bits());
        (u128::from(bits[1]) << 64 | u128::from(bits[0])) & all != all
    }

    /// Writes the call, as `name`, of `callee` in Peeper's convention, or
    /// of a runtime function, with `arguments`, after noting its
    /// declaration.
    fn write_call(
        &mut self,
        out: &mut String,
        name: &str,
        callee: Callee,
        signature: &Signature,
        arguments: &[Operand],
        location: &DebugLocation,
    ) {
        let callee = match callee {
            Callee::Function(function) => {
                let symbol = &self.symbols[&function];
                self.module
                    .declare(symbol, || declaration(symbol, signature));
                global(symbol)
            }
            Callee::Runtime(symbol) => {
                self.module
                    .declare(symbol, || declaration(symbol, signature));
                global(symbol)
            }
            Callee::Pointer(address) => self.operand(address).to_string(),
        };
        let (result, _) = peeper_types(signature);
        let arguments = comma_separated(arguments.iter().map(|&argument| self.typed(argument)));
        out.push_str("  ");
        if let Some(Passing::Register { .. }) = signature.result {
            _ = write!(out, "{name} = ");
        }
        _ = writeln!(out, "call {result} {callee}({arguments}){location}");
    }

    /// Writes the call, as `name`, of the C function `symbol` with
    /// `arguments`, each passed as `c` says, after noting its declaration.
    /// A value C takes or gives in memory or in pieces is the register's,
    /// resized to that memory as `signature` says it is signed.
    #[allow(clippy::too_many_arguments)] // each is part of the one call
    fn write_c_call(
        &mut self,
        out: &mut String,
        name: &str,
        symbol: &str,
        signature: &Signature,
        c: &CSignature,
        arguments: &[Operand],
        location: &DebugLocation,
    ) {
        // The memory a result C gives there or in pieces is read from.
        let returned = format!("{name}.r");
        let (mut declared, mut passed) = (Vec::new(), Vec::new());
        if let Some(CPassing::Memory { size, align, .. }) = c.result {
            _ = writeln!(
                self.memory,
                "  {returned} = alloca [{size} x i8], align {align}"
            );
            let parameter = format!("ptr sret([{size} x i8]) align {align}");
            passed.push(format!("{parameter} {returned}"));
            declared.push(parameter);
        }
        let parameters = arguments
            .iter()
            .zip(&c.parameters)
            .zip(&signature.parameters);
        for (index, ((&argument, &passing), &peeper)) in parameters.enumerate() {
            let Passing::Register { class, signed } = peeper else {
                unreachable!("only a scalar passes to C")
            };
            let (path, value) = (
                format!("{name}.{index}"),
                self.operand(argument).to_string(),
            );
            match passing {
                CPassing::Register { extension } => {
                    let parameter = c_register_type(class, extension, false);
                    passed.push(format!("{parameter} {value}"));
                    declared.push(parameter);
                }
                CPassing::Pieces { count, bits } => {
                    let width = count * bits;
                    let whole = format!("{path}.x");
                    let whole =
                        write_resized(out, &whole, &value, class.bits(), width, signed, location);
                    for piece in 0..count {
                        let shift = piece * bits;
                        _ = writeln!(
                            out,
                            "  {path}.s{piece} = lshr i{width} {whole}, {shift}{location}\n  \
                             {path}.p{piece} = trunc i{width} {path}.s{piece} to i{bits}{location}"
                        );
                        passed.push(format!("i{bits} {path}.p{piece}"));
                        declared.push(format!("i{bits}"));
                    }
                }
                // A copy C may change.
                CPassing::Memory {
                    size,
                    align,
                    by_value,
                } => {
                    _ = writeln!(
                        self.memory,
                        "  {path} = alloca [{size} x i8], align {align}"
                    );
                    let width = (size * 8) as u32;
                    let whole = format!("{path}.x");
                    let whole =
                        write_resized(out, &whole, &value, class.bits(), width, signed, location);
                    _ = writeln!(
                        out,
                        "  store i{width} {whole}, ptr {path}, align {align}{location}"
                    );
                    let parameter = if by_value {
                        format!("ptr byval([{size} x i8]) align {align}")
                    } else {
                        "ptr".to_string()
                    };
                    passed.push(format!("{parameter} {path}"));
                    declared.push(parameter);
                }
                CPassing::Vector { .. } => unreachable!("only a result is passed as a vector"),
            }
        }
        let result = match c.result {
            None | Some(CPassing::Memory { .. }) => "void".to_string(),
            Some(CPassing::Register { extension }) => {
                let Some(Passing::Register { class, .. }) = signature.result else {
                    unreachable!("C gives a scalar")
                };
                c_register_type(class, extension, true)
            }
            Some(CPassing::Pieces { count, bits }) => {
                let pieces = vec![format!("i{bits}"); count as usize];
                format!("{{ {} }}", pieces.join(", "))
            }
            Some(CPassing::Vector { count, bits }) => format!("<{count} x i{bits}>"),
        };
        let callee = global(symbol);
        self.module.declare(symbol, || {
            format!("declare {result} {callee}({})\n\n", declared.join(", "))
        });
        let call = format!("call {result} {callee}({})", passed.join(", "));
        // A result C gives in memory or in pieces is read back as one
        // integer of that memory's width.
        let (size, align) = match c.result {
            None => {
                _ = writeln!(out, "  {call}{location}");
                return;
            }
            Some(CPassing::Register { .. }) => {
                _ = writeln!(out, "  {name} = {call}{location}");
                return;
            }
            Some(CPassing::Memory { size, align, .. }) => {
                _ = writeln!(out, "  {call}{location}");
                (size, align)
            }
            Some(CPassing::Pieces { count, bits } | CPassing::Vector { count, bits }) => {
                let size = u64::from(count * bits / 8);
                _ = writeln!(self.memory, "  {returned} = alloca [{size} x i8], align 16");
                _ = writeln!(
                    out,
                    "  {name}.c = {call}{location}\n  \
                     store {result} {name}.c, ptr {returned}, align 16{location}"
                );
                (size, 16)
            }
        };
        let Some(Passing::Register { class, signed }) = signature.result else {
            unreachable!("C gives a scalar")
        };
        let width = (size * 8) as u32;
        let loaded = if width == class.bits() {
            name.to_string()
        } else {
            format!("{name}.w")
        };
        _ = writeln!(
            out,
            "  {loaded} = load i{width}, ptr {returned}, align {align}{location}"
        );
        if loaded != name {
            write_resized(out, name, &loaded, width, class.bits(), signed, location);
        }
    }

    /// Writes the calls that write `message` and where `span` is to the
    /// standard error and exit with `PANIC_EXIT_CODE`, then `unreachable`,
    /// with no newline after it. The text is known at compile time, so it is
    /// one string constant.
    fn write_panic(
        &mut self,
        out: &mut String,
        message: &[u8],
        span: Span,
        debug_scope: Id<DebugScope>,
    ) {
        let location = self.location(span, debug_scope);
        let text = panic_text(&self.module.program.root, self.file, message, span);
        let string = self.module.add_string(&text);
        let (stderr, length, code) = (
            STDERR.to_string(),
            text.len().to_string(),
            PANIC_EXIT_CODE.to_string(),
        );
        self.write_runtime_call(out, &PANIC_WRITE, &[&stderr, &string, &length], &location);
        self.write_runtime_call(out, &PANIC_EXIT, &[&code], &location);
        out.push_str("  unreachable");
    }

    /// Writes the call of the runtime function `signature` with
    /// `arguments`, each already LLVM text, one per parameter, after noting
    /// its declaration.
    fn write_runtime_call(
        &mut self,
        out: &mut String,
        signature: &runtime_abi::symbols::Signature,
        arguments: &[&str],
        location: &DebugLocation,
    ) {
        assert_eq!(arguments.len(), signature.parameters.len(), "{signature:?}");
        let pointer_bits = self.module.target.pointer_bits;
        let carrier_type = |carrier| match carrier {
            Carrier::I32 => "i32".to_string(),
            Carrier::Usize => format!("i{pointer_bits}"),
            Carrier::F64 => "double".to_string(),
            Carrier::Pointer => "ptr".to_string(),
        };
        let callee = global(signature.symbol);
        let result = signature.result.map_or("void".to_string(), carrier_type);
        let parameters: Vec<String> = signature
            .parameters
            .iter()
            .map(|&carrier| carrier_type(carrier))
            .collect();
        self.module.declare(signature.symbol, || {
            format!("declare {result} {callee}({})\n\n", parameters.join(", "))
        });
        let arguments = (parameters.iter().zip(arguments))
            .map(|(ty, value)| fmt::from_fn(move |out| write!(out, "{ty} {value}")));
        _ = writeln!(
            out,
            "  call {result} {callee}({}){location}",
            comma_separated(arguments)
        );
    }
}

/// The LLVM name of the address of `slot` of `body`: its `alloca`, or the
/// parameter it is when passed in memory (`Body::parameter_in_memory`).
/// Where a jump to each block goes: past each block that writes nothing
/// and only jumps on, such as an `if`'s join that ends a loop pass, or a
/// branch holding only `break`. The path checks keep these blocks in the
/// MIR; LLVM needs none of them. A jump into an endless loop of such blocks
/// stops on one of them.
fn jump_targets(body: &Body) -> Vec<Id<Block>> {
    let passed_through = |block: Id<Block>| {
        let data = &body.blocks[block];
        let Terminator::Jump(next) = data.terminator else {
            return None;
        };
        let writes =
            (body.lists.items(data.instructions)).any(|id| body.instructions[id].kind.runs());
        (!writes).then_some(next)
    };
    // Each block is followed once: a chain found is kept for every block on it.
    let mut targets: Vec<Option<Id<Block>>> = vec![None; body.blocks.len()];
    let mut on_path = vec![false; body.blocks.len()];
    let mut path = Vec::new();
    for start in body.blocks.ids() {
        let mut block = start;
        let target = loop {
            if let Some(known) = targets[block.index()] {
                break known;
            }
            match passed_through(block) {
                Some(next) if !on_path[block.index()] => {
                    on_path[block.index()] = true;
                    path.push(block);
                    block = next;
                }
                _ => break block,
            }
        };
        for passed in path.drain(..) {
            on_path[passed.index()] = false;
            targets[passed.index()] = Some(target);
        }
        targets[start.index()] = Some(target);
    }
    (targets.into_iter())
        .map(|target| target.expect("each block was followed"))
        .collect()
}

/// By block, whether the entry reaches it through the jumps as written
/// (`targets`). Code after a `return` or a `break` has a block in the MIR,
/// for the unreachable code warning, and none in LLVM.
fn reached_blocks(body: &Body, targets: &[Id<Block>]) -> Vec<bool> {
    let mut reached = vec![false; body.blocks.len()];
    let mut pending = vec![targets[0]];
    while let Some(block) = pending.pop() {
        if std::mem::replace(&mut reached[block.index()], true) {
            continue;
        }
        match body.blocks[block].terminator {
            Terminator::Jump(next) => pending.push(targets[next.index()]),
            Terminator::Branch {
                then, otherwise, ..
            } => pending.extend([targets[then.index()], targets[otherwise.index()]]),
            Terminator::Return { .. } | Terminator::Panic { .. } | Terminator::FallOff(_) => {}
        }
    }
    reached
}

/// By instruction, whether its value is read as a byte: by anything but a
/// `Branch`'s condition or a `Check`'s, which read a comparison's `i1`
/// (`Writer::write_truth`).
fn values_read(body: &Body) -> Vec<bool> {
    let mut operands = Vec::new();
    for id in body.instructions.ids() {
        let kind = &body.instructions[id].kind;
        if !matches!(kind, InstructionKind::Check { .. }) {
            kind.operands(|operand| operands.push(operand));
        }
        kind.accesses(|access| operands.extend(access.place.operands()));
    }
    for block in body.blocks.ids() {
        if let Terminator::Return {
            value: Some(value), ..
        } = body.blocks[block].terminator
        {
            operands.push(value);
        }
    }
    let mut read = vec![false; body.instructions.len()];
    for operand in operands {
        if let Operand::Value(id) = operand {
            read[id.index()] = true;
        }
    }
    read
}

fn slot_name(body: &Body, slot: Id<Slot>) -> String {
    match body.parameter_in_memory(slot) {
        Some(parameter) => format!("%a{parameter}"),
        None => format!("%s{}", slot.index()),
    }
}

/// `step` with a constant index as the offset it is, as `write_place`
/// writes it.
fn folded(step: Step) -> Step {
    match step {
        Step::Index {
            index: Operand::Constant { bits, .. },
            size,
            ..
        } => Step::Offset(bits[0] * size),
        step => step,
    }
}

/// Writes `value`, an integer of `from` bits, as one of `to` bits named
/// `name`, extended as `signed` says or cut, and gives its name; `value`
/// itself when the widths match.
fn write_resized(
    out: &mut String,
    name: &str,
    value: &str,
    from: u32,
    to: u32,
    signed: bool,
    location: &DebugLocation,
) -> String {
    let instruction = match (from.cmp(&to), signed) {
        (std::cmp::Ordering::Equal, _) => return value.to_string(),
        (std::cmp::Ordering::Greater, _) => "trunc",
        (std::cmp::Ordering::Less, true) => "sext",
        (std::cmp::Ordering::Less, false) => "zext",
    };
    _ = writeln!(
        out,
        "  {name} = {instruction} i{from} {value} to i{to}{location}"
    );
    name.to_string()
}
