//! What the checking tests share: a one-module program read from text. Its
//! declarations are checked one at a time; checking one that asks about
//! another needs the scheduler, so those tests live in the `build` crate.

use std::collections::HashMap;
use std::sync::Arc;

use model::DefId;
use model::ModuleId;
use model::constant::Constant;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
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

    fn signature(&mut self, function: DefId) -> TypeId {
        unreachable!("a check test asked for the signature of {function:?}; test it in `build`")
    }

    fn constant(&mut self, constant: DefId) -> Option<Constant> {
        unreachable!("a check test asked for the value of {constant:?}; test it in `build`")
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
        diagnostics: Vec::new(),
    }
}

/// A diagnostic as its code, message and the text of its first label.
pub(crate) type Reported = (&'static str, String, String);

/// Runs `work` on a checker of the module `app:main` written as `text`, which
/// imports `app:utils` as `utils`. Gives what `work` gives, the program's
/// types and the diagnostics.
pub(crate) fn with_checker<T>(
    text: &str,
    work: impl FnOnce(&mut Checker<Program>) -> T,
) -> (T, Types, Vec<Reported>) {
    let imports = vec![project::Import {
        path: "app:utils".to_string(),
        alias: Some("utils".to_string()),
        span: source::Span::new(0, 0),
    }];
    let utils = module("app:utils", "", Vec::new());
    let main = module("app:main", text, imports);
    let file = main.ast.file.clone();
    let (utils_id, main_id) = (ModuleId::from_index(0), ModuleId::from_index(1));
    let ids = HashMap::from([("app:utils", utils_id), ("app:main", main_id)]);
    let mut diagnostics = Vec::new();
    let scopes = [
        collect(utils_id, &utils, &ids, &mut diagnostics),
        collect(main_id, &main, &ids, &mut diagnostics),
    ];
    let mut program = Program {
        types: Types::new(),
    };
    let mut checker = Checker {
        module: &main,
        scopes: &scopes,
        scope: &scopes[1],
        prelude: None,
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
