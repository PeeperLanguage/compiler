//! The dumps `peeper -dump` writes: each module's THIR, MIR and LLVM IR as
//! text, one file per module and kind, for reading what the compiler made of
//! a program.

use std::io::Write;
use std::path::Path;

use arena::Id;
use model::DefId;
use model::mir;
use model::semantic_types::Types;
use model::thir;

/// An IR `peeper -dump` writes, one file per module.
#[derive(Clone, Copy)]
pub enum Dump {
    Thir,
    Mir,
    Ll,
}

impl Dump {
    pub const ALL: [Dump; 3] = [Dump::Thir, Dump::Mir, Dump::Ll];

    /// The dump file's extension, which `-dump` also takes as its mode.
    pub fn extension(self) -> &'static str {
        match self {
            Dump::Thir => "thir",
            Dump::Mir => "mir",
            Dump::Ll => "ll",
        }
    }
}

/// The text of each module's dumps, gathered while the program compiles and
/// written once it has no errors. The LLVM text comes from the program's
/// `llvm::Module`s, which the driver builds once for dumps and linking.
pub struct Dumps<'p> {
    program: &'p project::Program,
    kinds: &'p [Dump],
    texts: Vec<Vec<String>>, // by module, then by kind; empty for `Ll`
}

impl<'p> Dumps<'p> {
    /// No text yet for any of the `kinds` of `program`'s modules.
    pub fn new(program: &'p project::Program, kinds: &'p [Dump]) -> Dumps<'p> {
        Dumps {
            program,
            kinds,
            texts: vec![vec![String::new(); kinds.len()]; program.modules.len()],
        }
    }

    /// Whether the LLVM text is asked for.
    pub fn wants_ll(&self) -> bool {
        self.kinds.iter().any(|kind| matches!(kind, Dump::Ll))
    }

    /// Adds the function `definition`'s THIR and MIR to its module's dumps.
    pub fn add(&mut self, definition: DefId, thir: &thir::Body, mir: &mir::Body, types: &Types) {
        let program = self.program;
        let tree = |definition: DefId| &program.modules[definition.module.index()].ast;
        let name = |definition: DefId| {
            let tree = tree(definition);
            let declaration = tree.decls[Id::from_index(definition.index as usize)];
            declaration.name().text(&tree.file).to_string()
        };
        let file = &tree(definition).file;
        let texts = &mut self.texts[definition.module.index()];
        for (text, kind) in texts.iter_mut().zip(self.kinds) {
            let body = match kind {
                Dump::Thir => thir.text(types, file, name),
                Dump::Mir => mir.text(types, file, name),
                Dump::Ll => continue,
            };
            text.push_str("fn ");
            text.push_str(&name(definition));
            text.push_str(":\n");
            text.push_str(&body);
        }
    }

    /// Writes each module's dumps under `folder`, at its import path with `:`
    /// as a folder (`myapp:deep/x` to `myapp/deep/x.mir`), and names each file
    /// written in `out`; `ll` holds each module's LLVM text when asked for. A
    /// module with no functions gets no file.
    pub fn write(
        &self,
        folder: &Path,
        ll: &[llvm::Module],
        out: &mut impl Write,
    ) -> std::io::Result<()> {
        for (index, (module, texts)) in self.program.modules.iter().zip(&self.texts).enumerate() {
            for (kind, text) in self.kinds.iter().zip(texts) {
                let text = match kind {
                    Dump::Ll if ll[index].is_empty() => continue,
                    Dump::Ll => &ll[index].text(),
                    _ if text.is_empty() => continue,
                    _ => text,
                };
                let file = folder.join(format!(
                    "{}.{}",
                    module.path.replace(':', "/"),
                    kind.extension()
                ));
                std::fs::create_dir_all(file.parent().unwrap_or(folder))?;
                std::fs::write(&file, text)?;
                writeln!(out, "wrote {}", file.display())?;
            }
        }
        Ok(())
    }
}
