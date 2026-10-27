//! LLVM IR text for a module (docs/ir-design.md): one walk over each
//! function's MIR, shaped like clang's output at `-O0`, with debug info from
//! the spans MIR keeps in a debug build. Run-time traps panic through the prelude's `Write`
//! and `Exit`.

mod debug_info;
mod function;
mod layout;
pub mod link;
pub mod runtime;
mod text;
pub mod write;

use std::collections::BTreeMap;
use std::collections::HashMap;
use std::collections::HashSet;
use std::fmt::Write;
use std::path::Path;

use model::DefId;
use model::ModuleId;
use model::semantic_types::TypeId;
use source::SourceFile;

use crate::debug_info::Metadata;
use crate::text::quoted;
use crate::text::symbol;

/// What a program is built for: debugging, with debug info, or release,
/// without it.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum BuildMode {
    Debug,
    Release,
}

/// A machine a program is built for: one row of `TARGETS`.
#[derive(PartialEq, Eq, Debug)]
pub struct Target {
    /// Its name for `-target` and its runtime's folder.
    name: &'static str,
    /// Its architecture and system as `std::env::consts` spells them, which
    /// find the host's row; `os` also picks the executables' suffix and
    /// whether the math library is linked.
    arch: &'static str,
    os: &'static str,
    /// What clang's `--target` names it.
    triple: &'static str,
    /// How many bits its pointers, and so `usize`, have.
    pub pointer_bits: u32,
    /// The bytes an `i64`, an `f64` or another 33- to 64-bit integer aligns
    /// to in a struct.
    i64_align: u64,
}

/// Every machine a program can be built for. A new one is a row here, and
/// its runtime built with `build-runtime <folder> <name>`.
const TARGETS: [Target; 7] = [
    Target {
        name: "x86_64-linux",
        arch: "x86_64",
        os: "linux",
        triple: "x86_64-linux-gnu",
        pointer_bits: 64,
        i64_align: 8,
    },
    Target {
        name: "aarch64-linux",
        arch: "aarch64",
        os: "linux",
        triple: "aarch64-linux-gnu",
        pointer_bits: 64,
        i64_align: 8,
    },
    // A 64-bit x86 Linux runs it with its 32-bit C library.
    Target {
        name: "i686-linux",
        arch: "x86",
        os: "linux",
        triple: "i686-linux-gnu",
        pointer_bits: 32,
        i64_align: 4,
    },
    Target {
        name: "x86_64-macos",
        arch: "x86_64",
        os: "macos",
        triple: "x86_64-apple-macos",
        pointer_bits: 64,
        i64_align: 8,
    },
    Target {
        name: "aarch64-macos",
        arch: "aarch64",
        os: "macos",
        triple: "arm64-apple-macos",
        pointer_bits: 64,
        i64_align: 8,
    },
    Target {
        name: "x86_64-windows",
        arch: "x86_64",
        os: "windows",
        triple: "x86_64-w64-windows-gnu",
        pointer_bits: 64,
        i64_align: 8,
    },
    Target {
        name: "aarch64-windows",
        arch: "aarch64",
        os: "windows",
        triple: "aarch64-w64-windows-gnu",
        pointer_bits: 64,
        i64_align: 8,
    },
];

impl Target {
    /// The machine the compiler runs on, or the error saying it has no row.
    pub fn host() -> Result<&'static Target, String> {
        let (arch, os) = (std::env::consts::ARCH, std::env::consts::OS);
        TARGETS
            .iter()
            .find(|target| target.arch == arch && target.os == os)
            .ok_or_else(|| format!("peeper has no target for this machine, {arch}-{os}"))
    }

    /// The target named `name`, or the error naming them all.
    pub fn named(name: &str) -> Result<&'static Target, String> {
        TARGETS
            .iter()
            .find(|target| target.name == name)
            .ok_or_else(|| {
                let names: Vec<&str> = TARGETS.iter().map(|target| target.name).collect();
                format!(
                    "unknown target `{name}`; the targets are {}",
                    names.join(", ")
                )
            })
    }

    /// Clang's flag for compiling and linking code for it; none for the
    /// host, so the host's clang keeps its own triple and system files.
    fn clang_flag(&self) -> Option<String> {
        (Target::host() != Ok(self)).then(|| format!("--target={}", self.triple))
    }

    /// What its executables' names end with.
    pub fn executable_suffix(&self) -> &'static str {
        if self.os == "windows" { ".exe" } else { "" }
    }

    /// Its runtime library, in its folder in the folder `runtime` of an
    /// installation.
    pub fn runtime_library(&self, runtime: &Path) -> std::path::PathBuf {
        runtime.join(self.name).join(runtime::LIBRARY)
    }
}

/// The LLVM text of one module, built a function at a time.
pub struct Module<'p> {
    program: &'p project::Program,
    module: ModuleId,
    functions: String, // each `define`
    strings: String,   // each string constant's global
    /// The `define` of each array printer (`Function::array_printer`) and
    /// range filler (`Function::range_filler`).
    private_functions: String,
    /// The `declare` of each function called, by symbol; sorted, so the text
    /// is the same every run. Those the module defines are left out.
    declarations: BTreeMap<String, String>,
    written: Written,
    metadata: Option<Metadata>, // the debug info, in a debug build
    target: &'static Target,
}

/// What the module has written so far, so each is written once.
#[derive(Default)]
struct Written {
    strings: HashMap<Vec<u8>, usize>,       // each string's number
    array_printers: HashMap<TypeId, usize>, // each array type's printer number
    range_fillers: HashMap<TypeId, usize>,  // each element type's filler number
    functions: HashSet<String>,             // each `define`'s symbol
}

impl<'p> Module<'p> {
    /// The module `module` of `program`, with no functions yet, built in
    /// `mode`, for `target`.
    fn new(
        program: &'p project::Program,
        module: ModuleId,
        target: &'static Target,
        mode: BuildMode,
    ) -> Module<'p> {
        let metadata = match mode {
            BuildMode::Debug => {
                let path = Path::new(&program.modules[module.index()].ast.file.path);
                Some(Metadata::new(path))
            }
            BuildMode::Release => None,
        };
        Module {
            program,
            module,
            functions: String::new(),
            strings: String::new(),
            private_functions: String::new(),
            declarations: BTreeMap::new(),
            written: Written::default(),
            metadata,
            target,
        }
    }

    /// Adds the C `main` that starts the program at the function `main`:
    /// it gives `main`'s result as the exit code when `exit_code`, else 0.
    pub fn add_entry_point(&mut self, main: DefId, exit_code: bool) {
        let main = function::global(&symbol(self.program, main));
        let call = if exit_code {
            format!("  %code = call i32 {main}()\n  ret i32 %code")
        } else {
            format!("  call void {main}()\n  ret i32 0")
        };
        _ = writeln!(
            self.functions,
            "define i32 @main() {{\nentry:\n{call}\n}}\n"
        );
    }

    /// The source file the module is read from.
    fn file(&self) -> &'p SourceFile {
        &self.program.modules[self.module.index()].ast.file
    }

    /// Whether no function was added.
    pub fn is_empty(&self) -> bool {
        self.functions.is_empty()
    }

    /// The module's LLVM text.
    pub fn text(&self) -> String {
        let path = &self.file().path;
        let mut out = String::new();
        _ = writeln!(out, "source_filename = {}\n", quoted(path.as_bytes()));
        // Every slice type's: its elements' address and their count.
        _ = writeln!(
            out,
            "%slice = type {{ ptr, i{} }}\n",
            self.target.pointer_bits
        );
        out.push_str(&self.strings);
        if !self.strings.is_empty() {
            out.push('\n');
        }
        out.push_str(&self.functions);
        out.push_str(&self.private_functions);
        for (symbol, declaration) in &self.declarations {
            if !self.written.functions.contains(symbol) {
                out.push_str(declaration);
            }
        }
        let Some(metadata) = &self.metadata else {
            return out;
        };
        out.push_str("declare void @llvm.dbg.declare(metadata, metadata, metadata)\n\n");
        let version = metadata.count;
        _ = writeln!(out, "!llvm.dbg.cu = !{{!{}}}", metadata.unit);
        _ = writeln!(
            out,
            "!llvm.module.flags = !{{!{version}, !{}}}",
            version + 1
        );
        out.push_str(&metadata.text);
        _ = writeln!(out, "!{version} = !{{i32 7, !\"Dwarf Version\", i32 4}}");
        _ = writeln!(
            out,
            "!{} = !{{i32 2, !\"Debug Info Version\", i32 3}}",
            version + 1
        );
        out
    }

    /// A pointer to the string constant `bytes`, ended by a zero byte; each
    /// string gets one global.
    fn add_string(&mut self, bytes: &[u8]) -> String {
        let count = self.written.strings.len();
        let id = *self
            .written
            .strings
            .entry(bytes.to_vec())
            .or_insert_with(|| {
                let mut text = bytes.to_vec();
                text.push(0);
                _ = writeln!(
                    self.strings,
                    "@.str.{count} = private unnamed_addr constant [{} x i8] c{}",
                    text.len(),
                    quoted(&text)
                );
                count
            });
        format!("@.str.{id}")
    }

    /// Notes that the module calls `symbol`, declared by `declaration`.
    fn declare(&mut self, symbol: &str, declaration: impl FnOnce() -> String) {
        if !self.declarations.contains_key(symbol) {
            self.declarations.insert(symbol.to_string(), declaration());
        }
    }
}

#[cfg(test)]
mod tests;
