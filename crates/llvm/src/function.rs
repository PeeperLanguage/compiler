//! One function's `define`: a walk over its MIR blocks. Each MIR block is an
//! LLVM block (`bb3`); an operation that can trap at run time checks first
//! and continues in a new block (`bb3.1`), so a `phi` names the block where
//! its incoming MIR block ends.

use std::collections::HashMap;
use std::fmt;
use std::fmt::Display;
use std::fmt::Write;
use std::iter::Peekable;

use arena::Id;
use eval::ValueError;
use eval::trap_message;
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
use source::Span;

use crate::Module;
use crate::c_extension;
use crate::constant_text;
use crate::in_memory;
use crate::layout;
use crate::quoted;
use crate::symbol;
use crate::type_text;

/// The runtime functions a panic calls, declared in the prelude
/// (`core:global`) as `write` and `exit`.
const WRITE: &str = "peeper_rt_v1_write";
const EXIT: &str = "peeper_rt_v1_exit";
/// The runtime functions `print` calls, with their LLVM parameters.
const PRINT_TEXT: (&str, &str) = ("peeper_rt_v1_print_text", "ptr");
const PRINT_INT: (&str, &str) = ("peeper_rt_v1_print_int", "i32, i32, ...");
const PRINT_FLOAT: (&str, &str) = ("peeper_rt_v1_print_float", "double, i32");
const PRINT_POINTER: (&str, &str) = ("peeper_rt_v1_print_pointer", "ptr");
const FLUSH: (&str, &str) = ("peeper_rt_v1_flush", "");
/// The intrinsic that copies a value kept in memory (`in_memory`).
const MEMCPY: &str = "llvm.memcpy.p0.p0.i64";
/// The exit code of a program that panics, as in Rust.
const PANIC_EXIT_CODE: i32 = 101;

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
            scope_metadata: scope_metadata.unwrap_or_default(),
            locations: HashMap::new(),
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
            block: 0,
            checked: 0,
            returned: None,
            memory: String::new(),
        };
        // Written straight onto the module's functions, taken out while
        // `function` borrows the module.
        let mut out = std::mem::take(&mut function.module.functions);
        // A parameter kept in memory is the caller's private copy
        // (`write_call`), so it is the variable's memory.
        let arguments = (0..body.parameters).map(|index| {
            let ty = body.variables[Id::from_index(index)].ty;
            fmt::from_fn(move |out| match in_memory(types, ty) {
                true => write!(out, "ptr %v{index}"),
                false => write!(out, "{} %arg{index}", type_text(types, ty)),
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
        self.defined.insert(symbol);
    }

    /// Adds the debug info of the function `definition`, linked as `symbol`,
    /// that has the MIR `body`: its `DISubprogram`, the scope of everything
    /// in it, and a `DILexicalBlock` for each block that declares a variable. Gives the subprogram and the
    /// metadata of each MIR debug scope: its lexical block, else its parent's;
    /// `None` in a release build, which has no debug info.
    /// The function's block takes the subprogram when there are parameters,
    /// whose scope is the function.
    fn add_debug_info(
        &mut self,
        definition: DefId,
        symbol: &str,
        body: &mir::Body,
        types: &Types,
    ) -> Option<(usize, Vec<usize>)> {
        let pointer_bits = self.pointer_bits;
        let metadata = self.metadata.as_mut()?;
        let tree = &self.program.modules[definition.module.index()].ast;
        let name = tree.decls[tree.declaration(definition.index)].name();
        let (line, _) = tree.file.position(name.span.start as usize);
        let program = self.program;
        // As `add_function` passes them: a value kept in memory by its
        // address, a result kept in memory where a first parameter points,
        // so a debugger reads no result from the C ABI's registers.
        let mut debug_type = |ty| match metadata.debug_type(program, types, ty, pointer_bits) {
            Some(id) if in_memory(types, ty) => {
                let address = metadata.add(format_args!(
                    "!DIDerivedType(tag: DW_TAG_pointer_type, baseType: !{id}, size: {pointer_bits})"
                ));
                format!("!{address}")
            }
            Some(id) => format!("!{id}"),
            None => "null".to_string(),
        };
        let mut signature = match in_memory(types, body.result) {
            true => vec!["null".to_string(), debug_type(body.result)],
            false => vec![debug_type(body.result)],
        };
        for parameter in body.variables.ids().take(body.parameters) {
            signature.push(debug_type(body.variables[parameter].ty));
        }
        let subroutine = metadata.add(format_args!(
            "!DISubroutineType(types: !{{{}}})",
            signature.join(", ")
        ));
        let (file, unit) = (metadata.file, metadata.unit);
        let subprogram = metadata.add(format_args!(
            "distinct !DISubprogram(name: {}, linkageName: {}, scope: !{file}, file: !{file}, \
             line: {line}, type: !{subroutine}, scopeLine: {line}, spFlags: DISPFlagDefinition, \
             unit: !{unit})",
            quoted(name.text(&tree.file).as_bytes()),
            quoted(symbol.as_bytes()),
        ));
        let mut declares = vec![false; body.debug_scopes.len()];
        for variable in body.variables.ids() {
            declares[body.variables[variable].debug_scope.index()] = true;
        }
        if body.parameters > 0 {
            declares[0] = false;
        }
        // A block comes after its parent, so the parent's debug scope is known.
        let mut scope_metadata: Vec<usize> = Vec::with_capacity(declares.len());
        for scope in body.debug_scopes.ids() {
            let parent = body.debug_scopes[scope]
                .parent
                .map_or(subprogram, |parent| scope_metadata[parent.index()]);
            if !declares[scope.index()] {
                scope_metadata.push(parent);
                continue;
            }
            let (line, column) = tree
                .file
                .position(body.debug_scopes.span(scope).start as usize);
            scope_metadata.push(metadata.add(format_args!(
                "!DILexicalBlock(scope: !{parent}, file: !{file}, line: {line}, column: {column})"
            )));
        }
        Some((subprogram, scope_metadata))
    }
}

/// How many times the instruction `id` can trap at run time, so it checks
/// first: a division or shift of integers by a value not known at compile
/// time, each element of its place at an index not known then, and a
/// `Check`. Check rejects a constant that would trap.
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
        | InstructionKind::Address(ref place) => (place.projections.iter())
            .filter(|step| {
                matches!(step, mir::Projection::Index(index) if !matches!(index, Operand::Constant(_)))
            })
            .count(),
        InstructionKind::Check { .. } => 1,
        _ => 0,
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
    scope_metadata: Vec<usize>, // by MIR debug scope (`Module::add_debug_info`)
    locations: HashMap<(usize, u32), usize>, // each scope and start offset's DILocation
    panics: String,             // the blocks that call the panic function, after the others
    checks: Vec<usize>,         // how many checks each block writes (`traps`)
    block: usize,               // the MIR block being written
    checked: usize,             // its checks so far
    returned: Option<Id<mir::Instruction>>, // its value built in `%result` (`write_block`)
    /// The `alloca` of each instruction's value kept in memory (`in_memory`),
    /// named as the value, for the entry block, so a loop reuses it.
    // shortcut: one slot per such value, so a function copying a big array
    // many times needs that many copies of stack; share slots or move
    // without a copy when a real program hits the stack limit.
    memory: String,
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
            let (program, pointer_bits) = (self.module.program, self.module.pointer_bits);
            let debug_type = metadata
                .debug_type(program, self.types, ty, pointer_bits)
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
                self.scope_metadata[debug_scope.index()],
            ));
            let location = self.location(span, debug_scope);
            _ = writeln!(
                out,
                "  call void @llvm.dbg.declare(metadata ptr %v{index}, \
                 metadata !{declared}, metadata !DIExpression()){location}",
            );
        }
    }

    /// The DILocation of where `span` starts, in the MIR debug scope
    /// `debug_scope`, as the end of a line; nothing in a release build.
    fn location(&mut self, span: Span, debug_scope: Id<mir::DebugScope>) -> DebugLocation {
        let file = self.module.file();
        let Some(metadata) = &mut self.module.metadata else {
            return DebugLocation(None);
        };
        let scope = self.scope_metadata[debug_scope.index()];
        // An offset has one line and column, so it is the key: they are
        // found only for a new node.
        let id = *self
            .locations
            .entry((scope, span.start))
            .or_insert_with(|| {
                let (line, column) = file.position(span.start as usize);
                metadata.add(format_args!(
                    "!DILocation(line: {line}, column: {column}, scope: !{scope})"
                ))
            });
        DebugLocation(Some(id))
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
        self.block = block.index();
        self.checked = 0;
        _ = writeln!(out, "bb{}:", block.index());
        let (body, types) = (self.body, self.types);
        let mir = &body.blocks[block];
        // A value kept in memory that the block returns as soon as it is
        // made is built in `%result` (`value_address`).
        self.returned = match mir.terminator {
            Terminator::Return {
                value: Some(Operand::Value(value)),
                ..
            } if in_memory(types, body.result)
                && matches!(
                    body.instructions[value].kind,
                    InstructionKind::Load(_)
                        | InstructionKind::Move(_)
                        | InstructionKind::Aggregate(_)
                        | InstructionKind::Call { .. }
                )
                && (body.lists.items(mir.instructions))
                    .filter(|&id| body.instructions[id].kind.runs())
                    .last()
                    == Some(value) =>
            {
                Some(value)
            }
            _ => None,
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
                if self.returned.is_none() {
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
                self.write_place_address(out, &name, place, &location, id);
                return; // the address is the value
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
                self.write_call(out, id, function, arguments, rest);
                Ok(())
            }
            InstructionKind::Print {
                newline,
                ref values,
            } => {
                self.write_print(out, id, newline, values, &location);
                return; // each line has its location
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
            // In the memory of the value it is part of, which no one changes:
            // its address when it is kept in memory, else loaded from there.
            InstructionKind::Field { value, ref fields } => {
                let field_address = fmt::from_fn(|out| match in_memory(types, ty) {
                    true => write!(out, "{name}"),
                    false => write!(out, "{name}.f"),
                });
                _ = write!(
                    out,
                    "  {field_address} = getelementptr inbounds {}, ptr {}, i32 0",
                    type_text(types, self.body.operand_type(value)),
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

    /// Writes the runtime calls that print `values` for the `print` `id`,
    /// a space between each, then a newline when `newline`, then a flush.
    /// Text known at compile time (spaces, a struct's names, a constant
    /// string up to its NUL) is gathered into one string between values.
    fn write_print(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        newline: bool,
        values: &[Operand],
        location: &DebugLocation,
    ) {
        let mut text = Vec::new();
        for (index, &value) in values.iter().enumerate() {
            if index > 0 {
                text.push(b' ');
            }
            if let Operand::Constant(constant) = value
                && let Value::Bytes(bytes) = &self.body.constants[constant.index()].value
            {
                text.extend(bytes.split(|&byte| byte == 0).next().unwrap_or_default());
                continue;
            }
            let path = format!("{}.{index}", value_name(id));
            let ty = self.body.operand_type(value);
            let value = self.operand(value).to_string();
            self.write_printed(out, &mut text, &value, ty, &path, location);
        }
        if newline {
            text.push(b'\n');
        }
        self.write_print_text(out, &mut text, location);
        self.write_runtime_call(out, FLUSH, "", location);
    }

    /// Writes what prints `value`, of the type `ty`, after `text`; `path`
    /// starts the names of the values computed on the way.
    fn write_printed(
        &mut self,
        out: &mut String,
        text: &mut Vec<u8>,
        value: &str,
        ty: TypeId,
        path: &str,
        location: &DebugLocation,
    ) {
        let types = self.types;
        match types[ty] {
            // As 64-bit limbs, least significant first, of the value
            // extended to their width.
            Type::Integer(Integer { signed, bits }) => {
                let count = bits.div_ceil(64);
                let width = count * 64;
                let mut wide = value.to_string();
                if bits < width {
                    let extension = if signed { "sext" } else { "zext" };
                    _ = writeln!(
                        out,
                        "  {path}.x = {extension} i{bits} {value} to i{width}{location}"
                    );
                    wide = format!("{path}.x");
                }
                let mut arguments = format!("i32 {}, i32 {count}", i32::from(signed));
                if count == 1 {
                    _ = write!(arguments, ", i64 {wide}");
                } else {
                    for limb in 0..count {
                        let shift = limb * 64;
                        _ = writeln!(
                            out,
                            "  {path}.s{limb} = lshr i{width} {wide}, {shift}{location}\n  \
                             {path}.l{limb} = trunc i{width} {path}.s{limb} to i64{location}"
                        );
                        _ = write!(arguments, ", i64 {path}.l{limb}");
                    }
                }
                self.write_print_text(out, text, location);
                self.write_runtime_call(out, PRINT_INT, &arguments, location);
            }
            Type::Float(float) => {
                let (value, bits) = match float {
                    Float::F32 => {
                        _ = writeln!(out, "  {path}.x = fpext float {value} to double{location}");
                        (format!("{path}.x"), 32)
                    }
                    Float::F64 => (value.to_string(), 64),
                };
                self.write_print_text(out, text, location);
                let arguments = format!("double {value}, i32 {bits}");
                self.write_runtime_call(out, PRINT_FLOAT, &arguments, location);
            }
            Type::Bool => {
                let yes = self.module.add_string(b"true");
                let no = self.module.add_string(b"false");
                _ = writeln!(
                    out,
                    "  {path}.b = select i1 {value}, ptr {yes}, ptr {no}{location}"
                );
                self.write_print_text(out, text, location);
                let arguments = format!("ptr {path}.b");
                self.write_runtime_call(out, PRINT_TEXT, &arguments, location);
            }
            Type::CString => {
                self.write_print_text(out, text, location);
                self.write_runtime_call(out, PRINT_TEXT, &format!("ptr {value}"), location);
            }
            Type::RawPointer { .. } => {
                self.write_print_text(out, text, location);
                self.write_runtime_call(out, PRINT_POINTER, &format!("ptr {value}"), location);
            }
            // A value kept in memory is its address, which the reference is.
            Type::Reference { target, .. } if in_memory(types, target) => {
                self.write_printed(out, text, value, target, path, location);
            }
            Type::Reference { target, .. } => {
                let target_text = type_text(types, target);
                _ = writeln!(
                    out,
                    "  {path}.d = load {target_text}, ptr {value}{location}"
                );
                let referred = format!("{path}.d");
                self.write_printed(out, text, &referred, target, &referred, location);
            }
            Type::Struct {
                definition,
                ref fields,
            } => {
                let program = self.module.program;
                let (name, file) = project::declared_name(&program.modules, definition);
                text.extend_from_slice(name.text(file).as_bytes());
                let names = project::field_names(&program.modules, definition);
                let struct_text = type_text(types, ty);
                for (index, (field_name, &field)) in names.zip(fields).enumerate() {
                    text.extend_from_slice(if index == 0 { b" { " } else { b", " });
                    text.extend_from_slice(field_name.as_bytes());
                    text.extend_from_slice(b": ");
                    // Its address, then its value unless that is kept in memory.
                    let address = format!("{path}.p{index}");
                    _ = writeln!(
                        out,
                        "  {address} = getelementptr inbounds {struct_text}, ptr {value}, i32 0, \
                         i32 {index}{location}"
                    );
                    let field_value = format!("{path}.f{index}");
                    let field_value = match in_memory(types, field) {
                        true => address,
                        false => {
                            let field_text = type_text(types, field);
                            _ = writeln!(
                                out,
                                "  {field_value} = load {field_text}, ptr {address}{location}"
                            );
                            field_value
                        }
                    };
                    self.write_printed(out, text, &field_value, field, &field_value, location);
                }
                text.extend_from_slice(if fields.is_empty() { b" {}" } else { b" }" });
            }
            _ => unreachable!("check prints only numbers, `bool`, `cstr`, pointers and structs"),
        }
    }

    /// Writes the call that prints `text`, if any, and empties it.
    fn write_print_text(&mut self, out: &mut String, text: &mut Vec<u8>, location: &DebugLocation) {
        if text.is_empty() {
            return;
        }
        let string = self.module.add_string(text);
        text.clear();
        self.write_runtime_call(out, PRINT_TEXT, &format!("ptr {string}"), location);
    }

    /// Writes the call of the runtime function `(symbol, parameters)` with
    /// `arguments`, after noting the declaration the module needs for it.
    fn write_runtime_call(
        &mut self,
        out: &mut String,
        (symbol, parameters): (&str, &str),
        arguments: &str,
        location: &DebugLocation,
    ) {
        let callee = global(symbol);
        self.module.declare(symbol, || {
            format!("declare void {callee}({parameters})\n\n")
        });
        _ = writeln!(
            out,
            "  call void ({parameters}) {callee}({arguments}){location}"
        );
    }

    /// The address of `place` for the instruction `id`, after writing the
    /// lines that compute it: a whole variable's own, else `<id's name>.f`.
    fn write_address(
        &mut self,
        out: &mut String,
        place: &mir::Place,
        location: &DebugLocation,
        id: Id<mir::Instruction>,
    ) -> impl Display + use<> {
        let whole = place.whole_variable();
        let address = fmt::from_fn(move |out| match whole {
            Some(variable) => write!(out, "%v{}", variable.index()),
            None => write!(out, "{}.f", value_name(id)),
        });
        if whole.is_none() {
            self.write_place_address(out, &address, place, location, id);
        }
        address
    }

    /// Writes `value` to `address`: copied there when it is kept in memory
    /// (`in_memory`), else stored.
    fn write_store(
        &mut self,
        out: &mut String,
        address: &dyn Display,
        value: Operand,
        location: &DebugLocation,
    ) {
        let ty = self.body.operand_type(value);
        if in_memory(self.types, ty) {
            let from = self.operand(value).to_string();
            self.write_copy(out, address, &from, ty, location);
        } else {
            _ = writeln!(
                out,
                "  store {}, ptr {address}{location}",
                self.typed(value)
            );
        }
    }

    /// Writes the copy of a value of type `ty`, kept in memory, from the
    /// address `from` to `to`.
    fn write_copy(
        &mut self,
        out: &mut String,
        to: &dyn Display,
        from: &dyn Display,
        ty: TypeId,
        location: &DebugLocation,
    ) {
        self.module.declare(MEMCPY, || {
            format!("declare void @{MEMCPY}(ptr, ptr, i64, i1)\n\n")
        });
        // A number, not LLVM's `ptrtoint (getelementptr (T, ptr null, i32 1))`,
        // which clang's optimizer splits no value by (measured: twice the
        // `-release` build time of a program of structs).
        let (size, _) = layout(self.types, ty, self.module.pointer_bits);
        _ = writeln!(
            out,
            "  call void @{MEMCPY}(ptr {to}, ptr {from}, i64 {size}, i1 false){location}"
        );
    }

    /// The address the value of the instruction `id`, of type `ty`, kept in
    /// memory, is built at, before the `rest` of its block. When its only use
    /// comes next at an address known before, it is built there, saving a
    /// copy: `%result` for the block's return (`write_block`), or the place
    /// of a store right after it, whose address lines are written and which
    /// is taken from `rest`. Else its own memory, an `alloca` noted for the
    /// entry block and named as the value.
    fn value_address(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        ty: TypeId,
        rest: &mut Peekable<impl Iterator<Item = Id<mir::Instruction>>>,
    ) -> String {
        let body = self.body;
        if self.returned == Some(id) {
            return "%result".to_owned();
        }
        if let Some(&store) = rest.peek()
            && let InstructionKind::Store {
                ref place,
                value: Operand::Value(value),
            } = body.instructions[store].kind
            && value == id
            // Computed before the value, so it must not load a reference or
            // check an index.
            && !place.is_behind_reference()
            && traps(body, self.types, store) == 0
        {
            rest.next();
            let span = body.instructions.span(store);
            let location = self.location(span, body.instructions[store].debug_scope);
            return self.write_address(out, place, &location, store).to_string();
        }
        let (name, text) = (value_name(id), type_text(self.types, ty));
        _ = writeln!(self.memory, "  {name} = alloca {text}");
        name.to_string()
    }

    /// Writes the lines computing `address`, the address of `place`, for the
    /// instruction `id`: one `getelementptr` for each run of fields and
    /// elements, after a check of each index there not known at compile
    /// time, and before each dereference a load of the pointer there,
    /// `<address>.<n>` for the `n`th.
    fn write_place_address(
        &mut self,
        out: &mut String,
        address: &dyn Display,
        place: &mir::Place,
        location: &DebugLocation,
        id: Id<mir::Instruction>,
    ) {
        let variable = place.variable.index();
        let mut ty = self.body.variables[place.variable].ty;
        let (types, body) = (self.types, self.body);
        // Whether an element's index gets its own `i64`, named
        // `<address>.<run>.<step>`: one not known at compile time and not an
        // `i64` already.
        let renamed = |index: Operand| {
            !matches!(index, Operand::Constant(_))
                && !matches!(
                    types[body.operand_type(index)],
                    Type::Integer(Integer { bits: 64, .. })
                )
        };
        let mut runs = place
            .projections
            .split(|step| matches!(step, mir::Projection::Dereference))
            .enumerate()
            .peekable();
        while let Some((run, steps)) = runs.next() {
            let run_type = ty;
            // Each index not known at compile time, checked against its
            // array's length, and `renamed` when it is not an `i64`.
            for (position, &step) in steps.iter().enumerate() {
                ty = match (step, &self.types[ty]) {
                    (mir::Projection::Field(index), Type::Struct { fields, .. }) => {
                        fields[index as usize]
                    }
                    (
                        mir::Projection::Index(Operand::Constant(_)),
                        &Type::Array { element, .. },
                    ) => {
                        element // check found it inside the array
                    }
                    (mir::Projection::Index(index), &Type::Array { element, length }) => {
                        let Type::Integer(integer) = self.types[self.body.operand_type(index)]
                        else {
                            unreachable!("check takes only an integer index")
                        };
                        let (bits, value) = (integer.bits, self.operand(index).to_string());
                        let name = format!("{address}.{run}.{position}");
                        // Compared as an `i64`, or in its own type when wider; a
                        // negative index, extended, is above any length.
                        let compared = match bits {
                            _ if !renamed(index) => format!("i64 {value}"),
                            65.. => format!("i{bits} {value}"),
                            _ => {
                                let extension = if integer.signed { "sext" } else { "zext" };
                                _ = writeln!(
                                    out,
                                    "  {name} = {extension} i{bits} {value} to i64{location}"
                                );
                                format!("i64 {name}")
                            }
                        };
                        _ = writeln!(
                            out,
                            "  {name}.out = icmp uge {compared}, {length}{location}"
                        );
                        let message = format!("index out of bounds: the length is {length}");
                        self.check(out, &format!("{name}.out"), &message, id);
                        if bits > 64 {
                            _ = writeln!(out, "  {name} = trunc {compared} to i64{location}");
                        }
                        element
                    }
                    _ => unreachable!("a field is taken of a struct, an element of an array"),
                };
            }
            // The address so far: the variable's, or the pointer loaded last.
            let base = fmt::from_fn(|out| match run {
                0 => write!(out, "%v{variable}"),
                _ => write!(out, "{address}.{}", run - 1),
            });
            let last = runs.peek().is_none();
            let run_address = fmt::from_fn(|out| match last {
                true => write!(out, "{address}"),
                false => write!(out, "{address}.{run}.f"),
            });
            _ = write!(
                out,
                "  {run_address} = getelementptr inbounds {}, ptr {base}, i32 0",
                type_text(self.types, run_type)
            );
            for (position, &step) in steps.iter().enumerate() {
                _ = match step {
                    mir::Projection::Field(index) => write!(out, ", i32 {index}"),
                    mir::Projection::Index(index) if renamed(index) => {
                        write!(out, ", i64 {address}.{run}.{position}")
                    }
                    mir::Projection::Index(index) => write!(out, ", i64 {}", self.operand(index)),
                    mir::Projection::Dereference => unreachable!("split at each dereference"),
                };
            }
            _ = writeln!(out, "{location}");
            if !last {
                let Some(target) = self.types[ty].pointee() else {
                    unreachable!("a dereference is taken of a typed pointer")
                };
                _ = writeln!(
                    out,
                    "  {address}.{run} = load ptr, ptr {run_address}{location}"
                );
                ty = target;
            }
        }
    }

    /// Writes the line calling `function` with `arguments` for the
    /// instruction `id`, after noting the declaration the module needs for
    /// it; a result kept in memory is built where `value_address` says,
    /// before the `rest` of the block.
    fn write_call(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        function: DefId,
        arguments: &[Operand],
        rest: &mut Peekable<impl Iterator<Item = Id<mir::Instruction>>>,
    ) {
        let (ty, types, body) = (self.body.instructions[id].ty, self.types, self.body);
        let symbol = symbol(self.module.program, function);
        let callee = global(&symbol);
        // Only C code needs the C ABI's extension of narrow integers; it
        // follows a parameter's type and comes before a result's.
        let external = project::extern_symbol(&self.module.program.modules, function).is_some();
        let extension = |ty| c_extension(types, ty).filter(|_| external);
        // A result kept in memory is written where a first argument points
        // (`add_function`). An argument kept in memory is always a private
        // copy (a load's or move's, a struct's or array's value, a result,
        // or a field of one), which the callee may change.
        let returned_in_memory = in_memory(types, ty);
        let result = fmt::from_fn(|out| match extension(ty) {
            _ if returned_in_memory => out.write_str("void"),
            Some(extension) => write!(out, "{extension} {}", type_text(types, ty)),
            None => type_text(types, ty).fmt(out),
        });
        let parameter = |argument| {
            let ty = body.operand_type(argument);
            fmt::from_fn(move |out| match extension(ty) {
                Some(extension) => write!(out, "{} {extension}", type_text(types, ty)),
                None if in_memory(types, ty) => out.write_str("ptr"),
                None => type_text(types, ty).fmt(out),
            })
        };
        let separator = if arguments.is_empty() { "" } else { ", " };
        self.module.declare(&symbol, || {
            let parameters = comma_separated(arguments.iter().copied().map(parameter));
            let first = if returned_in_memory {
                ["ptr", separator].concat()
            } else {
                String::new()
            };
            format!("declare {result} {callee}({first}{parameters})\n\n")
        });
        let result_address = returned_in_memory.then(|| self.value_address(out, id, ty, rest));
        out.push_str("  ");
        if !returned_in_memory && ty != TypeId::VOID {
            _ = write!(out, "{} = ", value_name(id));
        }
        let function = &*self;
        let arguments = arguments.iter().map(|&argument| {
            let (parameter, value) = (parameter(argument), function.operand(argument));
            fmt::from_fn(move |out| write!(out, "{parameter} {value}"))
        });
        let arguments = comma_separated(arguments);
        if let Some(result_address) = result_address {
            _ = write!(
                out,
                "call {result} {callee}(ptr {result_address}{separator}{arguments})"
            );
        } else {
            _ = write!(out, "call {result} {callee}({arguments})");
        }
    }

    /// Writes the line computing the binary operation `id`, after the checks
    /// it needs first.
    fn write_binary(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        op: BinaryOp,
        left: Operand,
        right: Operand,
    ) {
        let name = value_name(id);
        let left_type = self.body.operand_type(left);
        let ty = type_text(self.types, left_type);
        let checks = traps(self.body, self.types, id) > 0;
        // The value the right operand was converted to, if it was.
        let mut converted: Option<String> = None;
        let instruction = match self.types[left_type] {
            Type::Float(_) => match op {
                BinaryOp::Add => "fadd",
                BinaryOp::Subtract => "fsub",
                BinaryOp::Multiply => "fmul",
                BinaryOp::Divide => "fdiv",
                BinaryOp::Remainder => "frem",
                BinaryOp::EqualTo => "fcmp oeq",
                BinaryOp::NotEqualTo => "fcmp une",
                BinaryOp::LessThan => "fcmp olt",
                BinaryOp::LessOrEqual => "fcmp ole",
                BinaryOp::GreaterThan => "fcmp ogt",
                BinaryOp::GreaterOrEqual => "fcmp oge",
                _ => unreachable!("check accepts no {op:?} of floats"),
            },
            ref integer => {
                let signed = matches!(integer, Type::Integer(integer) if integer.signed);
                match op {
                    BinaryOp::Divide | BinaryOp::Remainder => {
                        if checks {
                            let zero = format!("{name}.z");
                            _ = writeln!(out, "  {zero} = icmp eq {ty} {}, 0", self.operand(right));
                            let message = trap_message(ValueError::DivisionByZero, integer);
                            self.check(out, &zero, &message, id);
                        }
                        let (l, r) = (self.operand(left), self.operand(right));
                        if signed && (checks || self.is_minus_one(right)) {
                            // MIN / -1 is MIN and MIN % -1 is 0, as check
                            // folds them: divide by 1 instead and negate.
                            let minus_one = format!("{name}.n");
                            let divisor = format!("{name}.d");
                            _ = writeln!(
                                out,
                                "  {minus_one} = icmp eq {ty} {r}, -1\n  \
                                 {divisor} = select i1 {minus_one}, {ty} 1, {ty} {r}"
                            );
                            if op == BinaryOp::Divide {
                                let quotient = format!("{name}.q");
                                let negated = format!("{name}.m");
                                _ = write!(
                                    out,
                                    "  {quotient} = sdiv {ty} {l}, {divisor}\n  \
                                     {negated} = sub {ty} 0, {l}\n  \
                                     {name} = select i1 {minus_one}, {ty} {negated}, {ty} {quotient}"
                                );
                                return;
                            }
                            converted = Some(divisor);
                        }
                        match (op, signed) {
                            (BinaryOp::Divide, true) => "sdiv",
                            (BinaryOp::Divide, false) => "udiv",
                            (_, true) => "srem",
                            (_, false) => "urem",
                        }
                    }
                    BinaryOp::ShiftLeft | BinaryOp::ShiftRight => {
                        converted = self.shift_count(out, id, right, left_type, checks);
                        match (op, signed) {
                            (BinaryOp::ShiftLeft, _) => "shl",
                            (_, true) => "ashr",
                            (_, false) => "lshr",
                        }
                    }
                    BinaryOp::Add => "add",
                    BinaryOp::Subtract => "sub",
                    BinaryOp::Multiply => "mul",
                    BinaryOp::BitAnd | BinaryOp::LogicalAnd => "and",
                    BinaryOp::BitOr | BinaryOp::LogicalOr => "or",
                    BinaryOp::BitXor => "xor",
                    BinaryOp::EqualTo => "icmp eq",
                    BinaryOp::NotEqualTo => "icmp ne",
                    BinaryOp::LessThan if signed => "icmp slt",
                    BinaryOp::LessThan => "icmp ult",
                    BinaryOp::LessOrEqual if signed => "icmp sle",
                    BinaryOp::LessOrEqual => "icmp ule",
                    BinaryOp::GreaterThan if signed => "icmp sgt",
                    BinaryOp::GreaterThan => "icmp ugt",
                    BinaryOp::GreaterOrEqual if signed => "icmp sge",
                    BinaryOp::GreaterOrEqual => "icmp uge",
                }
            }
        };
        _ = write!(
            out,
            "  {name} = {instruction} {ty} {}, ",
            self.operand(left)
        );
        _ = match converted {
            Some(converted) => write!(out, "{converted}"),
            None => write!(out, "{}", self.operand(right)),
        };
    }

    /// Whether `operand` is the constant -1: every bit of its type set.
    fn is_minus_one(&self, operand: Operand) -> bool {
        let Operand::Constant(constant) = operand else {
            return false;
        };
        let constant = &self.body.constants[constant.index()];
        let (Value::Integer(limbs), Type::Integer(integer)) =
            (&constant.value, &self.types[constant.ty])
        else {
            return false;
        };
        let ones: u32 = limbs.as_slice().iter().map(|limb| limb.count_ones()).sum();
        ones == integer.bits
    }

    /// The count of a shift of a `shifted` value, converted to that type:
    /// the name of the converted value, or `None` when the count has that
    /// type already. A count not known at compile time is checked first to be
    /// from 0 to the width less one; the check compares at 32 bits or more,
    /// where the width fits and a negative count reads as too big
    /// (docs/ir-design.md, rule 4).
    fn shift_count(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        count: Operand,
        shifted: TypeId,
        checks: bool,
    ) -> Option<String> {
        let (Type::Integer(shifted_type), Type::Integer(count_type)) = (
            &self.types[shifted],
            &self.types[self.body.operand_type(count)],
        ) else {
            unreachable!("check accepts shifts of integers by integers only");
        };
        let (width, count_bits) = (shifted_type.bits, count_type.bits);
        let name = value_name(id);
        if checks {
            let too_far = format!("{name}.c");
            if count_bits < 32 {
                let conversion = if count_type.signed { "sext" } else { "zext" };
                _ = writeln!(
                    out,
                    "  {name}.e = {conversion} i{count_bits} {} to i32\n  \
                     {too_far} = icmp uge i32 {name}.e, {width}",
                    self.operand(count)
                );
            } else {
                _ = writeln!(
                    out,
                    "  {too_far} = icmp uge i{count_bits} {}, {width}",
                    self.operand(count)
                );
            }
            let message = trap_message(ValueError::ShiftOutOfRange, &self.types[shifted]);
            self.check(out, &too_far, &message, id);
        }
        if count_bits == width {
            return None;
        }
        let conversion = if count_bits < width { "zext" } else { "trunc" };
        _ = writeln!(
            out,
            "  {name}.s = {conversion} i{count_bits} {} to i{width}",
            self.operand(count)
        );
        Some(format!("{name}.s"))
    }

    /// Goes on in the next block of the MIR block being written when
    /// `failed` is false, else panics (`write_panic`) with `message` where
    /// the instruction `id` is written.
    fn check(&mut self, out: &mut String, failed: &str, message: &str, id: Id<mir::Instruction>) {
        self.checked += 1;
        let next = format!("bb{}.{}", self.block, self.checked);
        let panic = format!("{next}.panic");
        _ = writeln!(
            out,
            "  br i1 {failed}, label %{panic}, label %{next}\n{next}:"
        );
        let span = self.body.instructions.span(id);
        let debug_scope = self.body.instructions[id].debug_scope;
        let mut panics = std::mem::take(&mut self.panics);
        _ = writeln!(panics, "{panic}:");
        self.write_panic(&mut panics, message.as_bytes(), span, debug_scope);
        panics.push('\n');
        self.panics = panics;
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
        debug_scope: Id<mir::DebugScope>,
    ) {
        let location = self.location(span, debug_scope);
        let file = self.module.file();
        let (line, column) = file.position(span.start as usize);
        let mut text = b"panic: ".to_vec();
        text.extend_from_slice(message);
        let place = format!("\n  --> {}:{line}:{column}\n", file.path);
        text.extend_from_slice(place.as_bytes());
        let string = self.module.add_string(&text);
        self.module.declare(WRITE, || {
            format!("declare i32 {}(i32, ptr, i32)\n\n", global(WRITE))
        });
        self.module
            .declare(EXIT, || format!("declare void {}(i32)\n\n", global(EXIT)));
        _ = write!(
            out,
            "  call i32 {}(i32 2, ptr {string}, i32 {}){location}\n  \
             call void {}(i32 {PANIC_EXIT_CODE}){location}\n  unreachable",
            global(WRITE),
            text.len(),
            global(EXIT),
        );
    }
}

/// The `, !dbg !N` that ends a line with its DILocation `N`; nothing without
/// one.
struct DebugLocation(Option<usize>);

impl std::fmt::Display for DebugLocation {
    fn fmt(&self, out: &mut std::fmt::Formatter) -> std::fmt::Result {
        match self.0 {
            Some(id) => write!(out, ", !dbg !{id}"),
            None => Ok(()),
        }
    }
}
