//! LLVM IR text for a program, from its MIR after cleanup, and a native
//! build and run of it with the toolchain's own clang. It depends on `model`
//! only, so it knows a type by its facts alone, as the interpreter does:
//!
//! - a `Scalar` is one integer register of its size (`i32`, `i8` for a
//!   `bool`); a reference is one too, as its facts are an `isize`'s, so a
//!   `Borrow` turns its address into an integer (`ptrtoint`) and a
//!   `Dereference` loads the address back as a pointer;
//! - a value in `Memory` is the address of its own storage: an `alloca` of
//!   its size and alignment in the entry block, filled with `memcpy`;
//! - a field is the byte offset its facts give (`getelementptr i8`), so the
//!   layout is the facts', never LLVM's.
//!
//! Each function is `@f<id>`; a C `main` calls the program's `main` and
//! prints what it returns as unsigned bits, as `eval::run` gives it.

use std::fmt::Write;
use std::path::Path;
use std::process::Command;
use std::sync::atomic::AtomicUsize;
use std::sync::atomic::Ordering;

use model::facts::Carrier;
use model::facts::Facts;
use model::facts::Target;
use model::mir::Body;
use model::mir::InstructionKind;
use model::mir::Operand;
use model::mir::Operator;
use model::mir::Place;
use model::mir::Program;
use model::mir::Projection;
use model::mir::Terminator;
use model::types::TypeId;

const MEMCPY: &str = "llvm.memcpy.p0.p0.i64";

/// The LLVM text of `program`, whose `main` is the function `main`.
pub fn text(program: &Program, main: u32) -> String {
    let mut out = String::new();
    for (id, body) in program.functions.iter().enumerate() {
        let mut function = Function {
            program,
            body,
            out: String::new(),
            temporaries: 0,
        };
        function.write(id);
        out.push_str(&function.out);
    }
    let result = program.facts(program.functions[main as usize].result);
    _ = writeln!(
        out,
        "@format = private constant [6 x i8] c\"%llu\\0A\\00\"\n"
    );
    out.push_str("define i32 @main() {\n");
    match result.carrier {
        Carrier::Nothing => _ = writeln!(out, "  call void @f{main}()\n  %wide = add i64 0, 0"),
        _ if result.size == 8 => _ = writeln!(out, "  %wide = call i64 @f{main}()"),
        _ => {
            let register = register(result);
            _ = writeln!(out, "  %result = call {register} @f{main}()");
            _ = writeln!(out, "  %wide = zext {register} %result to i64");
        }
    }
    out.push_str("  call i32 (ptr, ...) @printf(ptr @format, i64 %wide)\n  ret i32 0\n}\n\n");
    out.push_str("declare i32 @printf(ptr, ...)\n");
    _ = writeln!(out, "declare void @{MEMCPY}(ptr, ptr, i64, i1)");
    out
}

/// The LLVM type a value with `facts` is carried in: a value in memory is
/// carried by its address.
fn register(facts: Facts) -> String {
    match facts.carrier {
        Carrier::Nothing => "void".to_string(),
        Carrier::Scalar => format!("i{}", facts.size * 8),
        Carrier::Memory => "ptr".to_string(),
        Carrier::Pair => unreachable!("a pair waits for slices and interfaces"),
    }
}

/// `value` read as a signed number of `bits`, so a constant always fits
/// its `iN`.
fn signed(value: u64, bits: u64) -> i64 {
    let unused = 64 - bits.min(64);
    ((value << unused) as i64) >> unused
}

/// Writing one function's `define`.
struct Function<'p> {
    program: &'p Program,
    body: &'p Body,
    out: String,
    temporaries: usize, // how many `%t` names are taken
}

impl Function<'_> {
    fn facts(&self, ty: TypeId) -> Facts {
        self.program.facts(ty)
    }

    fn pointer(&self) -> String {
        format!("i{}", self.program.pointer_bytes * 8)
    }

    /// A new name for a value with no MIR id.
    fn temporary(&mut self) -> String {
        self.temporaries += 1;
        format!("%t{}", self.temporaries)
    }

    fn memcpy(&mut self, to: &str, from: &str, facts: Facts) {
        let size = facts.size;
        _ = writeln!(
            self.out,
            "  call void @{MEMCPY}(ptr {to}, ptr {from}, i64 {size}, i1 false)"
        );
    }

    fn write(&mut self, id: usize) {
        let body = self.body;
        let parameters: Vec<String> = (body.variables[..body.parameters].iter().enumerate())
            .filter(|(_, variable)| self.facts(variable.ty).carrier != Carrier::Nothing)
            .map(|(index, variable)| format!("{} %a{index}", register(self.facts(variable.ty))))
            .collect();
        let result = register(self.facts(body.result));
        _ = writeln!(
            self.out,
            "define {result} @f{id}({}) {{\nentry:",
            parameters.join(", ")
        );
        // Every variable, and every value in memory, has its storage here,
        // so a loop's turns reuse it.
        let storage = (body
            .variables
            .iter()
            .map(|variable| ("v", variable.ty))
            .enumerate())
        .chain(
            body.instructions
                .iter()
                .map(|instruction| ("i", instruction.ty))
                .enumerate(),
        );
        for (index, (prefix, ty)) in storage {
            let facts = self.facts(ty);
            if prefix == "v" || facts.carrier == Carrier::Memory {
                let (size, align) = (facts.size, facts.align);
                _ = writeln!(
                    self.out,
                    "  %{prefix}{index} = alloca [{size} x i8], align {align}"
                );
            }
        }
        self.out.push_str("  br label %bb0\n");
        for (index, block) in body.blocks.iter().enumerate() {
            _ = writeln!(self.out, "bb{index}:");
            for &id in &block.instructions {
                self.instruction(id.0 as usize);
            }
            self.terminator(&block.terminator);
        }
        self.out.push_str("}\n\n");
    }

    /// The address of `place`: its variable's storage, then each step.
    fn address(&mut self, place: &Place) -> String {
        let mut at = format!("%v{}", place.variable.0);
        for projection in &place.projections {
            let next = self.temporary();
            match *projection {
                Projection::Field { offset, .. } => {
                    _ = writeln!(
                        self.out,
                        "  {next} = getelementptr i8, ptr {at}, i64 {offset}"
                    );
                }
                // The reference is stored as an integer; loaded as a
                // pointer, it is the address it holds.
                Projection::Dereference => _ = writeln!(self.out, "  {next} = load ptr, ptr {at}"),
            }
            at = next;
        }
        at
    }

    /// `operand`, of a value with `facts`, as LLVM writes it.
    fn operand(&mut self, operand: Operand, facts: Facts) -> String {
        match operand {
            Operand::Constant(value) => signed(value, facts.size * 8).to_string(),
            Operand::Argument(index) => format!("%a{index}"),
            Operand::Value(id) => format!("%i{}", id.0),
            Operand::Function(function) => {
                let name = self.temporary();
                let pointer = self.pointer();
                _ = writeln!(
                    self.out,
                    "  {name} = ptrtoint ptr @f{function} to {pointer}"
                );
                name
            }
        }
    }

    /// Writes `value`, of a value with `facts`, to the storage at `to`.
    fn put(&mut self, to: &str, value: Operand, facts: Facts) {
        let value = self.operand(value, facts);
        match facts.carrier {
            Carrier::Nothing => {}
            Carrier::Scalar => {
                let (register, align) = (register(facts), facts.align);
                _ = writeln!(
                    self.out,
                    "  store {register} {value}, ptr {to}, align {align}"
                );
            }
            Carrier::Memory => self.memcpy(to, &value, facts),
            Carrier::Pair => unreachable!("a pair waits for slices and interfaces"),
        }
    }

    fn instruction(&mut self, id: usize) {
        let instruction = &self.body.instructions[id];
        let facts = self.facts(instruction.ty);
        let name = format!("%i{id}");
        match &instruction.kind {
            InstructionKind::Load(place) | InstructionKind::Move(place) => {
                let at = self.address(place);
                match facts.carrier {
                    Carrier::Nothing => {}
                    Carrier::Scalar => {
                        let (register, align) = (register(facts), facts.align);
                        _ = writeln!(
                            self.out,
                            "  {name} = load {register}, ptr {at}, align {align}"
                        );
                    }
                    Carrier::Memory => self.memcpy(&name, &at, facts),
                    Carrier::Pair => unreachable!("a pair waits for slices and interfaces"),
                }
            }
            InstructionKind::Store { place, value, ty } => {
                let at = self.address(place);
                self.put(&at, *value, self.facts(*ty));
            }
            InstructionKind::Borrow { place, .. } => {
                let at = self.address(place);
                let pointer = self.pointer();
                _ = writeln!(self.out, "  {name} = ptrtoint ptr {at} to {pointer}");
            }
            InstructionKind::Binary {
                operator,
                left,
                right,
                operands,
            } => {
                let operands = self.facts(*operands);
                let left = self.operand(*left, operands);
                let right = self.operand(*right, operands);
                let register = register(operands);
                // No `nsw`: these wrap, as the interpreter's do.
                let instruction = match operator {
                    Operator::Add => "add",
                    Operator::Subtract => "sub",
                    Operator::Multiply => "mul",
                    Operator::LessThan => {
                        let compared = self.temporary();
                        let result = format!("i{}", facts.size * 8);
                        _ = writeln!(
                            self.out,
                            "  {compared} = icmp slt {register} {left}, {right}"
                        );
                        _ = writeln!(self.out, "  {name} = zext i1 {compared} to {result}");
                        return;
                    }
                };
                _ = writeln!(
                    self.out,
                    "  {name} = {instruction} {register} {left}, {right}"
                );
            }
            InstructionKind::Call {
                function,
                arguments,
            } => {
                let callee = &self.program.functions[*function as usize];
                let mut written = Vec::new();
                for (argument, parameter) in arguments.iter().zip(&callee.variables) {
                    let facts = self.facts(parameter.ty);
                    if facts.carrier != Carrier::Nothing {
                        let value = self.operand(*argument, facts);
                        written.push(format!("{} {value}", register(facts)));
                    }
                }
                let arguments = written.join(", ");
                match facts.carrier {
                    Carrier::Nothing => {
                        _ = writeln!(self.out, "  call void @f{function}({arguments})")
                    }
                    Carrier::Scalar => {
                        let register = register(facts);
                        _ = writeln!(
                            self.out,
                            "  {name} = call {register} @f{function}({arguments})"
                        );
                    }
                    Carrier::Memory | Carrier::Pair => {
                        unreachable!("the checker refuses a function returning a value in memory")
                    }
                }
            }
            InstructionKind::Aggregate(parts) => {
                for part in parts {
                    let at = self.temporary();
                    let offset = part.offset;
                    _ = writeln!(
                        self.out,
                        "  {at} = getelementptr i8, ptr {name}, i64 {offset}"
                    );
                    self.put(&at, part.value, self.facts(part.ty));
                }
            }
            InstructionKind::ScopeEnd(_) => unreachable!("cleanup removes scope ends"),
        }
    }

    fn terminator(&mut self, terminator: &Terminator) {
        match *terminator {
            Terminator::Goto(to) => _ = writeln!(self.out, "  br label %bb{}", to.0),
            Terminator::Branch {
                condition,
                then,
                otherwise,
                ..
            } => {
                let condition = self.operand(condition, Facts::scalar(1));
                let truth = self.temporary();
                _ = writeln!(self.out, "  {truth} = icmp ne i8 {condition}, 0");
                _ = writeln!(
                    self.out,
                    "  br i1 {truth}, label %bb{}, label %bb{}",
                    then.0, otherwise.0
                );
            }
            Terminator::Return { value, .. } => {
                let facts = self.facts(self.body.result);
                match (value, facts.carrier) {
                    (Some(value), Carrier::Scalar) => {
                        let value = self.operand(value, facts);
                        _ = writeln!(self.out, "  ret {} {value}", register(facts));
                    }
                    _ => self.out.push_str("  ret void\n"),
                }
            }
            Terminator::FallOff(_) => match self.facts(self.body.result).carrier {
                Carrier::Nothing => self.out.push_str("  ret void\n"),
                // The path checks refuse a function giving a value that
                // reaches its closing brace.
                _ => self.out.push_str("  unreachable\n"),
            },
        }
    }
}

/// The toolchain's own clang; never one on the PATH.
const CLANG: &str = concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../../build/toolchains/native/bin/clang"
);

/// Builds `text` for `target` with clang into a program, runs it, and gives
/// the number it prints. The files live under the demo's `target` folder
/// and are removed after.
// shortcut: an 8-byte target is this machine and a 4-byte one is
// i686-linux, which holds on the x86-64 Linux the demo is built on; name a
// triple in `Target` when another machine builds it.
pub fn run(text: &str, target: Target) -> Result<u64, String> {
    static BUILDS: AtomicUsize = AtomicUsize::new(0);
    let folder = Path::new(env!("CARGO_MANIFEST_DIR")).join("../target/native");
    std::fs::create_dir_all(&folder).map_err(|error| format!("{}: {error}", folder.display()))?;
    let name = format!(
        "{}-{}",
        std::process::id(),
        BUILDS.fetch_add(1, Ordering::Relaxed)
    );
    let source = folder.join(format!("{name}.ll"));
    let program = folder.join(name);
    let ran = std::fs::write(&source, text)
        .map_err(|error| format!("{}: {error}", source.display()))
        .and_then(|()| {
            let mut clang = Command::new(CLANG);
            clang
                .arg("-fuse-ld=lld")
                .args(["-x", "ir"])
                .arg(&source)
                .arg("-o")
                .arg(&program);
            if target.pointer_bytes == 4 {
                clang.arg("--target=i686-linux-gnu");
            }
            output(&mut clang)
        })
        .and_then(|_| output(&mut Command::new(&program)))
        .and_then(|printed| {
            let printed = String::from_utf8_lossy(&printed);
            (printed.trim().parse())
                .map_err(|error| format!("the program printed {printed:?}, not a number: {error}"))
        });
    _ = std::fs::remove_file(&source);
    _ = std::fs::remove_file(&program);
    ran
}

/// What `command` prints, or why it failed with what it printed as errors.
fn output(command: &mut Command) -> Result<Vec<u8>, String> {
    let program = command.get_program().to_string_lossy().into_owned();
    let output = command
        .output()
        .map_err(|error| format!("cannot run {program}: {error}"))?;
    if output.status.success() {
        return Ok(output.stdout);
    }
    Err(format!(
        "{program} failed ({})\n{}",
        output.status,
        String::from_utf8_lossy(&output.stderr).trim_end()
    ))
}
