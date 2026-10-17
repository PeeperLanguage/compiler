//! The dumps `peeper -dump` writes: each module's THIR and MIR as text, one
//! file per module and kind, for reading what the compiler made of a program.

use std::io::Write;
use std::path::Path;

use arena::Id;
use model::DefId;
use model::mir;
use model::thir;
use model::types::Types;

/// An IR `peeper -dump` writes, one file per module.
#[derive(Clone, Copy)]
pub enum Dump {
    Thir,
    Mir,
}

impl Dump {
    pub const ALL: [Dump; 2] = [Dump::Thir, Dump::Mir];

    /// The dump file's extension, which `-dump` also takes as its mode.
    pub fn extension(self) -> &'static str {
        match self {
            Dump::Thir => "thir",
            Dump::Mir => "mir",
        }
    }
}

/// The text of each module's dumps, gathered while the program compiles and
/// written once it has no errors.
pub struct Dumps<'p> {
    program: &'p project::Program,
    kinds: &'p [Dump],
    texts: Vec<String>, // by module, then by kind
}

impl<'p> Dumps<'p> {
    /// No text yet for any of the `kinds` of `program`'s modules.
    pub fn new(program: &'p project::Program, kinds: &'p [Dump]) -> Dumps<'p> {
        Dumps {
            program,
            kinds,
            texts: vec![String::new(); program.order.len() * kinds.len()],
        }
    }

    /// Adds the function `definition`'s THIR and MIR to its module's dumps.
    pub fn add(&mut self, definition: DefId, thir: &thir::Body, mir: &mir::Body, types: &Types) {
        let program = self.program;
        let tree =
            |definition: DefId| &program.modules[&program.order[definition.module as usize]].syntax;
        let name = |definition: DefId| {
            let tree = tree(definition);
            let declaration = tree.decls[Id::from_index(definition.index as usize)];
            declaration.name().text(&tree.file).to_string()
        };
        let file = &tree(definition).file;
        for (index, kind) in self.kinds.iter().enumerate() {
            let text = &mut self.texts[definition.module as usize * self.kinds.len() + index];
            text.push_str("fn ");
            text.push_str(&name(definition));
            text.push_str(":\n");
            text.push_str(&match kind {
                Dump::Thir => thir.text(types, file, name),
                Dump::Mir => mir.text(types, file, name),
            });
        }
    }

    /// Writes each module's dumps under `folder`, at its import path with `:`
    /// as a folder (`myapp:deep/x` to `myapp/deep/x.mir`), and names each file
    /// written in `out`. A module with no functions gets no file.
    pub fn write(&self, folder: &Path, out: &mut impl Write) -> std::io::Result<()> {
        for (module, path) in self.program.order.iter().enumerate() {
            for (index, kind) in self.kinds.iter().enumerate() {
                let text = &self.texts[module * self.kinds.len() + index];
                if text.is_empty() {
                    continue;
                }
                let file = folder.join(format!("{}.{}", path.replace(':', "/"), kind.extension()));
                std::fs::create_dir_all(file.parent().unwrap_or(folder))?;
                std::fs::write(&file, text)?;
                writeln!(out, "wrote {}", file.display())?;
            }
        }
        Ok(())
    }
}
