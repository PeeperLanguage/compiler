//! What the checking tests share: a one-module program read from text. Its
//! declarations are checked one at a time; checking one that asks about
//! another needs the scheduler, so those tests live in the `build` crate.

use std::cell::OnceCell;
use std::sync::Arc;

use model::DefId;
use model::ModuleId;
use model::constant::Constant;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use project::ImportKind;
use source::SourceFile;

use crate::Checker;
use crate::Queries;
use crate::scope::collect;

pub(crate) struct Program {
    pub(crate) types: Types,
}

impl Queries for Program {
    fn types(&mut self) -> &mut Types {
        &mut self.types
    }

    fn pointer_bits(&self) -> u32 {
        64
    }

    fn function(&mut self, definition: DefId) -> TypeId {
        unreachable!("a check test asked for {definition:?}; test it in `build`")
    }

    fn constant(&mut self, definition: DefId) -> Option<Constant> {
        unreachable!("a check test asked for {definition:?}; test it in `build`")
    }
}

/// The module `path`, written as `text`, with `imports`; its text must parse
/// without errors.
pub(crate) fn module(path: &str, text: &str, imports: Vec<project::Import>) -> project::Module {
    let file = Arc::new(SourceFile::new("main.peep", text));
    let (ast, parse) = parser::parse(&file);
    assert!(parse.is_empty(), "{parse:?}");
    project::Module {
        path: path.to_string(),
        ast,
        imports,
        scope: OnceCell::new(),
    }
}

/// A diagnostic as its code, message and the text of its first label.
pub(crate) type Reported = (&'static str, String, String);

/// Runs `work` on a checker of the module `app:main` written as `text`, which
/// imports `app:utils` as `utils` (an import added after `text`, so the
/// places in `text` stay as written). Gives what `work` gives, the program's
/// types and the diagnostics.
pub(crate) fn with_checker<T>(
    text: &str,
    work: impl FnOnce(&mut Checker<Program>) -> T,
) -> (T, Types, Vec<Reported>) {
    let (utils_id, main_id) = (ModuleId::from_index(0), ModuleId::from_index(1));
    let imports = vec![project::Import {
        kind: ImportKind::Declared(0),
        module: utils_id,
    }];
    let text = format!("{text}\nimport \"app:utils\";\n");
    let modules = [
        module("app:utils", "", Vec::new()),
        module("app:main", &text, imports),
    ];
    let file = modules[1].ast.file.clone();
    let mut diagnostics = Vec::new();
    for id in [utils_id, main_id] {
        let scope = collect(id, &modules, &mut diagnostics);
        assert!(modules[id.index()].scope.set(scope).is_ok());
    }
    let mut program = Program {
        types: Types::new(),
    };
    let mut checker = Checker {
        modules: &modules,
        current_module: main_id,
        queries: &mut program,
        diagnostics: &mut diagnostics,
    };
    let result = work(&mut checker);
    let reported = diagnostics
        .iter()
        .map(|diagnostic| {
            let text = file.slice(diagnostic.labels[0].span).to_string();
            (diagnostic.code.id, diagnostic.message.clone(), text)
        })
        .collect();
    (result, program.types, reported)
}

/// `expected` as `Reported`s, to compare with what was reported.
pub(crate) fn reported(expected: &[(&'static str, &str, &str)]) -> Vec<Reported> {
    let owned = |&(code, message, text): &(&'static str, &str, &str)| {
        (code, message.to_string(), text.to_string())
    };
    expected.iter().map(owned).collect()
}
