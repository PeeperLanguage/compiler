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
use std::collections::HashSet;
use std::fmt::Write;
use std::path::Path;

use model::DefId;
use model::ModuleId;
use model::machine::Machine;
use model::semantic_types::TypeId;
use rustc_hash::FxHashMap;
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

/// Clang's flag for compiling and linking code for `target`; none for the
/// host, so the host's clang keeps its own triple and system files.
fn clang_flag(target: &Machine) -> Option<String> {
    (Machine::host() != Ok(target)).then(|| format!("--target={}", target.triple))
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
    target: &'static Machine,
}

/// What the module has written so far, so each is written once.
#[derive(Default)]
struct Written {
    strings: FxHashMap<Vec<u8>, usize>,       // each string's number
    array_printers: FxHashMap<TypeId, usize>, // each array type's printer number
    range_fillers: FxHashMap<TypeId, usize>,  // each element type's filler number
    functions: HashSet<String>,               // each `define`'s symbol
}

impl<'p> Module<'p> {
    /// The module `module` of `program`, with no functions yet, built in
    /// `mode`, for `target`.
    fn new(
        program: &'p project::Program,
        module: ModuleId,
        target: &'static Machine,
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
