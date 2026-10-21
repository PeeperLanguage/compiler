//! One function's `define`: a walk over its MIR blocks. Each MIR block is an
//! LLVM block (`bb3`); an operation that can trap at run time checks first
//! and continues in a new block (`bb3.1`), so a `phi` names the block where
//! its incoming MIR block ends.

use std::collections::HashMap;
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
    pub fn function(&mut self, definition: DefId, ty: TypeId, body: &mir::Body, types: &Types) {
        let Type::Function { parameters, result } = &types[ty] else {
            unreachable!("a function's signature is a function type");
        };
        let program = self.program;
        let tree = &program.modules[definition.module.index()].ast;
        let symbol = symbol(program, definition);
        let scope = self.add_debug_info(definition, &symbol, parameters, *result, types);
        // Each string constant's global.
        let strings = body
            .constants
            .iter()
            .map(|constant| match &constant.value {
                Value::Bytes(bytes) => Some(self.string(bytes)),
                _ => None,
            })
            .collect();
        let mut function = Function {
            module: self,
            strings,
            body,
            types,
            file: &tree.file,
            scope,
            scopes: Vec::new(),
            locations: HashMap::new(),
            out: String::new(),
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
        let arguments: Vec<String> = parameters
            .iter()
            .enumerate()
            .map(|(index, &parameter)| format!("{} %arg{index}", type_text(types, parameter)))
            .collect();
        _ = writeln!(
            function.out,
            "define {} {}({}) !dbg !{scope} {{\nentry:",
            type_text(types, *result),
            global(&symbol),
            arguments.join(", ")
        );
        function.variables(parameters.len());
        function.out.push_str("  br label %bb0\n");
        for block in body.blocks.ids() {
            function.block(block, *result);
        }
        let Function { out, panics, .. } = function;
        self.functions.push_str(&out);
        self.functions.push_str(&panics);
        self.functions.push_str("}\n\n");
        self.defined.insert(symbol);
    }

    /// Adds the debug info of the function `definition`, linked as `symbol`,
    /// that takes `parameters` and gives `result`, and gives the id of its
    /// `DISubprogram`: the scope of everything in the function.
    fn add_debug_info(
        &mut self,
        definition: DefId,
        symbol: &str,
        parameters: &[TypeId],
        result: TypeId,
        types: &Types,
    ) -> usize {
        let tree = &self.program.modules[definition.module.index()].ast;
        let name = tree.decls[tree.declaration(definition.index)].name();
        let (line, _) = tree.file.position(name.span.start as usize);
        let mut signature = vec![match self.debug_type(types, result) {
            Some(id) => format!("!{id}"),
            None => "null".to_string(),
        }];
        for &parameter in parameters {
            let id = self.debug_type(types, parameter).unwrap_or_default();
            signature.push(format!("!{id}"));
        }
        let subroutine = self.metadata.add(&format!(
            "!DISubroutineType(types: !{{{}}})",
            signature.join(", ")
        ));
        self.metadata.add(&format!(
            "distinct !DISubprogram(name: {}, linkageName: {}, scope: !{file}, file: !{file}, \
             line: {line}, type: !{subroutine}, scopeLine: {line}, spFlags: DISPFlagDefinition, \
             unit: !{})",
            quoted(name.text(&tree.file).as_bytes()),
            quoted(symbol.as_bytes()),
            self.metadata.unit,
            file = self.metadata.file,
        ))
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

/// `symbol` as an LLVM global name.
pub(crate) fn global(symbol: &str) -> String {
    format!("@{}", quoted(symbol.as_bytes()))
}

/// Writing one function's `define`.
struct Function<'m, 'p, 'b> {
    module: &'m mut Module<'p>,
    strings: Vec<Option<String>>, // by constant: the global of each string
    body: &'b mir::Body,
    types: &'b Types,
    file: &'b SourceFile,
    scope: usize, // the function's DISubprogram
    /// The `DILexicalBlock` of each block that declares a variable (not the
    /// function's block when it has parameters, whose scope is the function),
    /// by its span, outer blocks first.
    scopes: Vec<(Span, usize)>,
    locations: HashMap<(usize, usize, usize), usize>, // each scope, line and column's DILocation
    out: String,
    panics: String,     // the blocks that call the panic function, after the others
    checks: Vec<usize>, // how many instructions of each block check first (`traps`)
    block: usize,       // the MIR block being written
    checked: usize,     // its checks so far
}

impl Function<'_, '_, '_> {
    /// The `alloca` and debug declaration of each variable, the first
    /// `parameters` of them the parameters.
    fn variables(&mut self, parameters: usize) {
        // A block gets a lexical block only when it declares a variable; the
        // parameters' scope is the function itself.
        let mut declares = vec![false; self.body.scopes.len()];
        for variable in self.body.variables.ids() {
            declares[self.body.variables[variable].scope.index()] = true;
        }
        if parameters > 0 {
            declares[0] = false;
        }
        // The debug scope of each block: its lexical block, else its parent's.
        // A block comes after its parent, so the parent's is known.
        let mut debug_scopes: Vec<usize> = Vec::with_capacity(declares.len());
        for scope in self.body.scopes.ids() {
            let parent = self.body.scopes[scope]
                .parent
                .map_or(self.scope, |parent| debug_scopes[parent.index()]);
            if !declares[scope.index()] {
                debug_scopes.push(parent);
                continue;
            }
            let block = self.body.scopes.span(scope);
            let (line, column) = self.file.position(block.start as usize);
            let id = self.module.metadata.add(&format!(
                "!DILexicalBlock(scope: !{parent}, file: !{}, line: {line}, column: {column})",
                self.module.metadata.file
            ));
            self.scopes.push((block, id));
            debug_scopes.push(id);
        }
        for variable in self.body.variables.ids() {
            let index = variable.index();
            let ty = self.body.variables[variable].ty;
            let span = self.body.variables.span(variable);
            let debug_type = self.module.debug_type(self.types, ty).unwrap_or_default();
            let (line, _) = self.file.position(span.start as usize);
            let argument = if index < parameters {
                format!("arg: {}, ", index + 1)
            } else {
                String::new()
            };
            let declared = self.module.metadata.add(&format!(
                "!DILocalVariable(name: {}, {argument}scope: !{}, file: !{}, line: {line}, type: !{debug_type})",
                quoted(self.file.slice(span).as_bytes()),
                debug_scopes[self.body.variables[variable].scope.index()],
                self.module.metadata.file,
            ));
            let location = self.location(span);
            _ = writeln!(
                self.out,
                "  %v{index} = alloca {}\n  call void @llvm.dbg.declare(metadata ptr %v{index}, \
                 metadata !{declared}, metadata !DIExpression()), !dbg !{location}",
                type_text(self.types, ty)
            );
        }
    }

    /// The innermost scope holding where `span` starts: a block that declares
    /// a variable, else the function.
    fn scope_of(&self, span: Span) -> usize {
        let inside =
            |&&(block, _): &&(Span, usize)| block.start <= span.start && span.start < block.end;
        self.scopes
            .iter()
            .rev()
            .find(inside)
            .map_or(self.scope, |&(_, id)| id)
    }

    /// The DILocation of where `span` starts.
    fn location(&mut self, span: Span) -> usize {
        let (line, column) = self.file.position(span.start as usize);
        let scope = self.scope_of(span);
        if let Some(&id) = self.locations.get(&(scope, line, column)) {
            return id;
        }
        let id = self.module.metadata.add(&format!(
            "!DILocation(line: {line}, column: {column}, scope: !{scope})"
        ));
        self.locations.insert((scope, line, column), id);
        id
    }

    /// The label of the LLVM block where the MIR block `block` ends: after
    /// its last check.
    fn end_label(&self, block: Id<mir::Block>) -> String {
        match self.checks[block.index()] {
            0 => format!("bb{}", block.index()),
            checks => format!("bb{}.{checks}", block.index()),
        }
    }

    fn operand(&self, operand: Operand) -> String {
        match operand {
            Operand::Constant(constant) => match &self.strings[constant.index()] {
                Some(global) => global.clone(),
                None => constant_text(self.types, &self.body.constants[constant.index()]),
            },
            Operand::Argument(index) => format!("%arg{index}"),
            Operand::Value(instruction) => format!("%i{}", instruction.index()),
        }
    }

    /// `operand` with its type before it, as a call argument.
    fn typed(&self, operand: Operand) -> String {
        let ty = operand_type(self.body, operand);
        format!("{} {}", type_text(self.types, ty), self.operand(operand))
    }

    /// Writes the block `block` of a function whose result type is `result`.
    fn block(&mut self, block: Id<mir::Block>, result: TypeId) {
        self.block = block.index();
        self.checked = 0;
        _ = writeln!(self.out, "bb{}:", block.index());
        let body = self.body;
        let mir = &body.blocks[block];
        for instruction in body.lists.items(mir.instructions) {
            self.instruction(instruction);
        }
        let line = match mir.terminator {
            Terminator::Goto(target) => format!("br label %bb{}", target.index()),
            Terminator::Branch {
                condition,
                span,
                then,
                otherwise,
            } => {
                let location = self.location(span);
                format!(
                    "br i1 {}, label %bb{}, label %bb{}, !dbg !{location}",
                    self.operand(condition),
                    then.index(),
                    otherwise.index()
                )
            }
            Terminator::Return { value, span } => {
                let location = self.location(span);
                match value {
                    Some(value) => format!("ret {}, !dbg !{location}", self.typed(value)),
                    None => format!("ret void, !dbg !{location}"),
                }
            }
            // Reachable with a result only in a function with an error,
            // which is not lowered.
            Terminator::FallOff(_) if result != TypeId::VOID => "unreachable".to_string(),
            Terminator::FallOff(span) => {
                let location = self.location(span);
                format!("ret void, !dbg !{location}")
            }
        };
        _ = writeln!(self.out, "  {line}");
    }

    /// Writes the instruction `id`.
    fn instruction(&mut self, id: Id<mir::Instruction>) {
        let mir::Instruction { ty, ref kind } = self.body.instructions[id];
        let span = self.body.instructions.span(id);
        let location = self.location(span);
        let name = format!("%i{}", id.index());
        let types = self.types;
        let line = match *kind {
            InstructionKind::Load(variable) => format!(
                "{name} = load {}, ptr %v{}",
                type_text(types, ty),
                variable.index()
            ),
            InstructionKind::Store { variable, value } => format!(
                "store {} {}, ptr %v{}",
                type_text(types, self.body.variables[variable].ty),
                self.operand(value),
                variable.index()
            ),
            InstructionKind::Binary { op, left, right } => self.binary(id, op, left, right, span),
            InstructionKind::Unary { op, value } => {
                let text = type_text(types, ty);
                let value = self.operand(value);
                match (op, &types[ty]) {
                    (UnaryOp::Negate, Type::Float(_)) => format!("{name} = fneg {text} {value}"),
                    (UnaryOp::Negate, _) => format!("{name} = sub {text} 0, {value}"),
                    (UnaryOp::LogicalNot, _) => format!("{name} = xor {text} {value}, true"),
                    (UnaryOp::BitNot, _) => format!("{name} = xor {text} {value}, -1"),
                }
            }
            InstructionKind::Widen(value) => {
                let from = operand_type(self.body, value);
                let conversion = match types[from] {
                    Type::Integer(integer) if integer.signed => "sext",
                    Type::Integer(_) => "zext",
                    _ => "fpext",
                };
                format!(
                    "{name} = {conversion} {} to {}",
                    self.typed(value),
                    type_text(types, ty)
                )
            }
            InstructionKind::Call {
                function,
                ref arguments,
            } => {
                let callee = symbol(self.module.program, function);
                // Only C code needs the C ABI's extension of narrow integers;
                // it follows a parameter's type and comes before a result's.
                let external = extern_symbol(self.module.program, function).is_some();
                let extension = |ty| {
                    if external { c_extension(types, ty) } else { "" }
                };
                let parameter = |ty| {
                    format!("{} {}", type_text(types, ty), extension(ty))
                        .trim_end()
                        .to_string()
                };
                let result = format!("{} {}", extension(ty), type_text(types, ty))
                    .trim_start()
                    .to_string();
                let body = self.body;
                self.module.declare(&callee, || {
                    let parameters: Vec<String> = arguments
                        .iter()
                        .map(|&argument| parameter(operand_type(body, argument)))
                        .collect();
                    format!(
                        "declare {result} {}({})\n\n",
                        global(&callee),
                        parameters.join(", ")
                    )
                });
                let arguments: Vec<String> = arguments
                    .iter()
                    .map(|&argument| {
                        let ty = operand_type(body, argument);
                        format!("{} {}", parameter(ty), self.operand(argument))
                    })
                    .collect();
                let call = format!(
                    "call {result} {}({})",
                    global(&callee),
                    arguments.join(", ")
                );
                if ty == TypeId::VOID {
                    call
                } else {
                    format!("{name} = {call}")
                }
            }
            InstructionKind::Phi(ref incoming) => {
                let incoming: Vec<String> = incoming
                    .iter()
                    .map(|&(block, value)| {
                        format!("[ {}, %{} ]", self.operand(value), self.end_label(block))
                    })
                    .collect();
                let line = format!(
                    "{name} = phi {} {}",
                    type_text(types, ty),
                    incoming.join(", ")
                );
                _ = writeln!(self.out, "  {line}");
                return; // no location: it runs where control arrives
            }
        };
        _ = writeln!(self.out, "  {line}, !dbg !{location}");
    }

    /// The line computing the binary operation `id`, after writing the checks
    /// it needs first.
    fn binary(
        &mut self,
        id: Id<mir::Instruction>,
        op: BinaryOp,
        left: Operand,
        right: Operand,
        span: Span,
    ) -> String {
        let name = format!("%i{}", id.index());
        let left_type = operand_type(self.body, left);
        let ty = type_text(self.types, left_type);
        let (l, mut r) = (self.operand(left), self.operand(right));
        let checks = traps(self.body, self.types, id);
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
                            _ = writeln!(self.out, "  {zero} = icmp eq {ty} {r}, 0");
                            self.check(&zero, "division by zero", span);
                        }
                        if signed && (checks || self.is_minus_one(right)) {
                            // MIN / -1 is MIN and MIN % -1 is 0, as check
                            // folds them: divide by 1 instead and negate.
                            let minus_one = format!("{name}.n");
                            let divisor = format!("{name}.d");
                            _ = writeln!(
                                self.out,
                                "  {minus_one} = icmp eq {ty} {r}, -1\n  \
                                 {divisor} = select i1 {minus_one}, {ty} 1, {ty} {r}"
                            );
                            if op == BinaryOp::Divide {
                                let quotient = format!("{name}.q");
                                let negated = format!("{name}.m");
                                _ = writeln!(
                                    self.out,
                                    "  {quotient} = sdiv {ty} {l}, {divisor}\n  \
                                     {negated} = sub {ty} 0, {l}"
                                );
                                return format!(
                                    "{name} = select i1 {minus_one}, {ty} {negated}, {ty} {quotient}"
                                );
                            }
                            r = divisor;
                        }
                        match (op, signed) {
                            (BinaryOp::Divide, true) => "sdiv",
                            (BinaryOp::Divide, false) => "udiv",
                            (_, true) => "srem",
                            (_, false) => "urem",
                        }
                    }
                    BinaryOp::ShiftLeft | BinaryOp::ShiftRight => {
                        r = self.shift_count(id, right, left_type, span, checks);
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
        format!("{name} = {instruction} {ty} {l}, {r}")
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

    /// The count of a shift of a `shifted` value, converted to that type.
    /// A count not known at compile time is checked first to be from 0 to
    /// the width less one; the check compares at 32 bits or more, where the
    /// width fits and a negative count reads as too big (docs/ir-design.md,
    /// rule 4).
    fn shift_count(
        &mut self,
        id: Id<mir::Instruction>,
        count: Operand,
        shifted: TypeId,
        span: Span,
        checks: bool,
    ) -> String {
        let (Type::Integer(shifted_type), Type::Integer(count_type)) = (
            &self.types[shifted],
            &self.types[operand_type(self.body, count)],
        ) else {
            unreachable!("check accepts shifts of integers by integers only");
        };
        let (width, count_bits) = (shifted_type.bits, count_type.bits);
        let value = self.operand(count);
        if checks {
            let (compared, compared_bits) = if count_bits < 32 {
                let extended = format!("%i{}.e", id.index());
                let conversion = if count_type.signed { "sext" } else { "zext" };
                _ = writeln!(
                    self.out,
                    "  {extended} = {conversion} i{count_bits} {value} to i32"
                );
                (extended, 32)
            } else {
                (value.clone(), count_bits)
            };
            let too_far = format!("%i{}.c", id.index());
            _ = writeln!(
                self.out,
                "  {too_far} = icmp uge i{compared_bits} {compared}, {width}"
            );
            let message = format!("a shift count must be from 0 to {}", width - 1);
            self.check(&too_far, &message, span);
        }
        if count_bits == width {
            return value;
        }
        let converted = format!("%i{}.s", id.index());
        let conversion = if count_bits < width { "zext" } else { "trunc" };
        _ = writeln!(
            self.out,
            "  {converted} = {conversion} i{count_bits} {value} to i{width}"
        );
        converted
    }

    /// Goes on in the next block of the MIR block being written when
    /// `failed` is false, else panics: writes `message` and where `span` is
    /// to the standard error and exits with `PANIC_EXIT_CODE`. The text is
    /// known at compile time, so it is one string constant.
    fn check(&mut self, failed: &str, message: &str, span: Span) {
        self.checked += 1;
        let next = format!("bb{}.{}", self.block, self.checked);
        let panic = format!("{next}.panic");
        _ = writeln!(
            self.out,
            "  br i1 {failed}, label %{panic}, label %{next}\n{next}:"
        );
        let location = self.location(span);
        let (line, column) = self.file.position(span.start as usize);
        let text = format!(
            "panic: {message}\n  --> {}:{line}:{column}\n",
            self.file.path
        );
        let string = self.module.string(text.as_bytes());
        self.module.declare(WRITE, || {
            format!("declare i32 {}(i32, ptr, i32)\n\n", global(WRITE))
        });
        self.module
            .declare(EXIT, || format!("declare void {}(i32)\n\n", global(EXIT)));
        _ = writeln!(
            self.panics,
            "{panic}:\n  call i32 {}(i32 2, ptr {string}, i32 {}), !dbg !{location}\n  \
             call void {}(i32 {PANIC_EXIT_CODE}), !dbg !{location}\n  unreachable",
            global(WRITE),
            text.len(),
            global(EXIT),
        );
    }
}
