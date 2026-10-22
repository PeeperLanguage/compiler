//! One function's `define`: a walk over its MIR blocks. Each MIR block is an
//! LLVM block (`bb3`); an operation that can trap at run time checks first
//! and continues in a new block (`bb3.1`), so a `phi` names the block where
//! its incoming MIR block ends.

use std::collections::HashMap;
use std::fmt;
use std::fmt::Display;
use std::fmt::Write;

use arena::Id;
use model::BinaryOp;
use model::DefId;
use model::UnaryOp;
use model::constant::Value;
use model::mir;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Terminator;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use source::SourceFile;
use source::Span;

use crate::Module;
use crate::c_extension;
use crate::constant_text;
use crate::extern_symbol;
use crate::quoted;
use crate::symbol;
use crate::type_text;

/// The runtime functions a panic calls, declared in the prelude
/// (`core:global`) as `write` and `exit`.
const WRITE: &str = "peeper_rt_v1_write";
const EXIT: &str = "peeper_rt_v1_exit";
/// The exit code of a program that panics, as in Rust.
const PANIC_EXIT_CODE: i32 = 101;

impl Module<'_> {
    /// Adds the function `definition` of this module, whose signature is `ty`
    /// and whose MIR is `body`.
    pub fn add_function(&mut self, definition: DefId, ty: TypeId, body: &mir::Body, types: &Types) {
        let Type::Function { parameters, result } = &types[ty] else {
            unreachable!("a function's signature is a function type");
        };
        let program = self.program;
        let tree = &program.modules[definition.module.index()].ast;
        let symbol = symbol(program, definition);
        let (subprogram, scope_metadata) = self
            .add_debug_info(definition, &symbol, parameters, *result, body, types)
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
            file: &tree.file,
            scope_metadata: scope_metadata.unwrap_or_default(),
            locations: HashMap::new(),
            panics: String::new(),
            checks: body
                .blocks
                .ids()
                .map(|block| {
                    let block = &body.blocks[block];
                    let instructions = body.lists.items(block.instructions);
                    instructions.filter(|&id| traps(body, types, id)).count()
                })
                .collect(),
            block: 0,
            checked: 0,
        };
        let mut out = String::new();
        let arguments = parameters.iter().enumerate().map(|(index, &parameter)| {
            fmt::from_fn(move |out| write!(out, "{} %arg{index}", type_text(types, parameter)))
        });
        _ = write!(
            out,
            "define {} {}({})",
            type_text(types, *result),
            global(&symbol),
            comma_separated(arguments)
        );
        if let Some(subprogram) = subprogram {
            _ = write!(out, " !dbg !{subprogram}");
        }
        out.push_str(" {\nentry:\n");
        function.variables(&mut out, parameters.len());
        out.push_str("  br label %bb0\n");
        for block in body.blocks.ids() {
            function.write_block(&mut out, block, *result);
        }
        let Function { panics, .. } = function;
        self.functions.push_str(&out);
        self.functions.push_str(&panics);
        self.functions.push_str("}\n\n");
        self.defined.insert(symbol);
    }

    /// Adds the debug info of the function `definition`, linked as `symbol`,
    /// that takes `parameters`, gives `result` and has the MIR `body`: its
    /// `DISubprogram`, the scope of everything in it, and a `DILexicalBlock`
    /// for each block that declares a variable. Gives the subprogram and the
    /// metadata of each MIR debug scope: its lexical block, else its parent's;
    /// `None` in a release build, which has no debug info.
    /// The function's block takes the subprogram when there are parameters,
    /// whose scope is the function.
    fn add_debug_info(
        &mut self,
        definition: DefId,
        symbol: &str,
        parameters: &[TypeId],
        result: TypeId,
        body: &mir::Body,
        types: &Types,
    ) -> Option<(usize, Vec<usize>)> {
        let metadata = self.metadata.as_mut()?;
        let tree = &self.program.modules[definition.module.index()].ast;
        let name = tree.decls[tree.declaration(definition.index)].name();
        let (line, _) = tree.file.position(name.span.start as usize);
        let mut signature = vec![match metadata.debug_type(types, result) {
            Some(id) => format!("!{id}"),
            None => "null".to_string(),
        }];
        for &parameter in parameters {
            let id = metadata.debug_type(types, parameter).unwrap_or_default();
            signature.push(format!("!{id}"));
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
        if !parameters.is_empty() {
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

/// Whether the instruction `id` can trap at run time, so it checks first: a
/// division or shift of integers by a value not known at compile time.
/// Check rejects a constant that would trap.
fn traps(body: &mir::Body, types: &Types, id: Id<mir::Instruction>) -> bool {
    let InstructionKind::Binary { op, left, right } = body.instructions[id].kind else {
        return false;
    };
    let divides_or_shifts = matches!(
        op,
        BinaryOp::Divide | BinaryOp::Remainder | BinaryOp::ShiftLeft | BinaryOp::ShiftRight
    );
    divides_or_shifts
        && !matches!(right, Operand::Constant(_))
        && matches!(types[operand_type(body, left)], Type::Integer(_))
}

/// The type of `operand`'s value.
fn operand_type(body: &mir::Body, operand: Operand) -> TypeId {
    match operand {
        Operand::Constant(constant) => body.constants[constant.index()].ty,
        Operand::Argument(index) => body.variables[Id::from_index(index as usize)].ty,
        Operand::Value(instruction) => body.instructions[instruction].ty,
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
    file: &'b SourceFile,
    scope_metadata: Vec<usize>, // by MIR debug scope (`Module::add_debug_info`)
    locations: HashMap<(usize, u32), usize>, // each scope and start offset's DILocation
    panics: String,             // the blocks that call the panic function, after the others
    checks: Vec<usize>,         // how many instructions of each block check first (`traps`)
    block: usize,               // the MIR block being written
    checked: usize,             // its checks so far
}

impl Function<'_, '_, '_> {
    /// The `alloca` of each variable and, in a debug build, its debug
    /// declaration; the first `parameters` of them are the parameters.
    fn variables(&mut self, out: &mut String, parameters: usize) {
        for variable in self.body.variables.ids() {
            let index = variable.index();
            let mir::Variable {
                ty, debug_scope, ..
            } = self.body.variables[variable];
            _ = writeln!(out, "  %v{index} = alloca {}", type_text(self.types, ty));
            let Some(metadata) = &mut self.module.metadata else {
                continue;
            };
            let span = self.body.variables.span(variable);
            let debug_type = metadata.debug_type(self.types, ty).unwrap_or_default();
            let (line, _) = self.file.position(span.start as usize);
            let argument = if index < parameters {
                format!("arg: {}, ", index + 1)
            } else {
                String::new()
            };
            let file = metadata.file;
            let declared = metadata.add(format_args!(
                "!DILocalVariable(name: {}, {argument}scope: !{}, file: !{file}, line: {line}, type: !{debug_type})",
                quoted(self.file.slice(span).as_bytes()),
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
                let (line, column) = self.file.position(span.start as usize);
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
        let ty = operand_type(self.body, operand);
        fmt::from_fn(move |out| {
            write!(
                out,
                "{} {}",
                type_text(self.types, ty),
                self.operand(operand)
            )
        })
    }

    /// Writes the block `block` of a function whose result type is `result`.
    fn write_block(&mut self, out: &mut String, block: Id<mir::Block>, result: TypeId) {
        self.block = block.index();
        self.checked = 0;
        _ = writeln!(out, "bb{}:", block.index());
        let body = self.body;
        let mir = &body.blocks[block];
        for instruction in body.lists.items(mir.instructions) {
            self.write_instruction(out, instruction);
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
            } => {
                _ = write!(out, "  ret {}", self.typed(value));
                Some(span)
            }
            // Reachable with a result only in a function with an error,
            // which is not lowered.
            Terminator::FallOff(_) if result != TypeId::VOID => {
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

    /// Writes the instruction `id`.
    fn write_instruction(&mut self, out: &mut String, id: Id<mir::Instruction>) {
        let mir::Instruction {
            ty,
            ref kind,
            debug_scope,
        } = self.body.instructions[id];
        let span = self.body.instructions.span(id);
        let location = self.location(span, debug_scope);
        let name = value_name(id);
        let types = self.types;
        _ = match *kind {
            InstructionKind::Load(variable) => write!(
                out,
                "  {name} = load {}, ptr %v{}",
                type_text(types, ty),
                variable.index()
            ),
            InstructionKind::Store { variable, value } => write!(
                out,
                "  store {} {}, ptr %v{}",
                type_text(types, self.body.variables[variable].ty),
                self.operand(value),
                variable.index()
            ),
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
            InstructionKind::Widen(value) => {
                let from = operand_type(self.body, value);
                let conversion = match types[from] {
                    Type::Integer(integer) if integer.signed => "sext",
                    Type::Integer(_) => "zext",
                    _ => "fpext",
                };
                write!(
                    out,
                    "  {name} = {conversion} {} to {}",
                    self.typed(value),
                    type_text(types, ty)
                )
            }
            InstructionKind::Call {
                function,
                ref arguments,
            } => {
                self.write_call(out, id, function, arguments);
                Ok(())
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
        };
        _ = writeln!(out, "{location}");
    }

    /// Writes the line calling `function` with `arguments` for the
    /// instruction `id`, after noting the declaration the module needs for
    /// it.
    fn write_call(
        &mut self,
        out: &mut String,
        id: Id<mir::Instruction>,
        function: DefId,
        arguments: &[Operand],
    ) {
        let (ty, types, body) = (self.body.instructions[id].ty, self.types, self.body);
        let symbol = symbol(self.module.program, function);
        let callee = global(&symbol);
        // Only C code needs the C ABI's extension of narrow integers; it
        // follows a parameter's type and comes before a result's.
        let external = extern_symbol(self.module.program, function).is_some();
        let extension = |ty| c_extension(types, ty).filter(|_| external);
        let result = fmt::from_fn(|out| match extension(ty) {
            Some(extension) => write!(out, "{extension} {}", type_text(types, ty)),
            None => type_text(types, ty).fmt(out),
        });
        let parameter = |argument| {
            let ty = operand_type(body, argument);
            fmt::from_fn(move |out| match extension(ty) {
                Some(extension) => write!(out, "{} {extension}", type_text(types, ty)),
                None => type_text(types, ty).fmt(out),
            })
        };
        self.module.declare(&symbol, || {
            let parameters = comma_separated(arguments.iter().copied().map(parameter));
            format!("declare {result} {callee}({parameters})\n\n")
        });
        out.push_str("  ");
        if ty != TypeId::VOID {
            _ = write!(out, "{} = ", value_name(id));
        }
        let function = &*self;
        let arguments = arguments.iter().map(|&argument| {
            let (parameter, value) = (parameter(argument), function.operand(argument));
            fmt::from_fn(move |out| write!(out, "{parameter} {value}"))
        });
        _ = write!(
            out,
            "call {result} {callee}({})",
            comma_separated(arguments)
        );
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
        let left_type = operand_type(self.body, left);
        let ty = type_text(self.types, left_type);
        let checks = traps(self.body, self.types, id);
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
                            self.check(out, &zero, "division by zero", id);
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
            &self.types[operand_type(self.body, count)],
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
            let message = format!("a shift count must be from 0 to {}", width - 1);
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
    /// `failed` is false, else panics: writes `message` and where the
    /// instruction `id` is written to the standard error and exits with `PANIC_EXIT_CODE`. The text is
    /// known at compile time, so it is one string constant.
    fn check(&mut self, out: &mut String, failed: &str, message: &str, id: Id<mir::Instruction>) {
        self.checked += 1;
        let next = format!("bb{}.{}", self.block, self.checked);
        let panic = format!("{next}.panic");
        _ = writeln!(
            out,
            "  br i1 {failed}, label %{panic}, label %{next}\n{next}:"
        );
        let span = self.body.instructions.span(id);
        let location = self.location(span, self.body.instructions[id].debug_scope);
        let (line, column) = self.file.position(span.start as usize);
        let text = format!(
            "panic: {message}\n  --> {}:{line}:{column}\n",
            self.file.path
        );
        let string = self.module.add_string(text.as_bytes());
        self.module.declare(WRITE, || {
            format!("declare i32 {}(i32, ptr, i32)\n\n", global(WRITE))
        });
        self.module
            .declare(EXIT, || format!("declare void {}(i32)\n\n", global(EXIT)));
        _ = writeln!(
            self.panics,
            "{panic}:\n  call i32 {}(i32 2, ptr {string}, i32 {}){location}\n  \
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
