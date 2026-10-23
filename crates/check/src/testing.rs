//! What the checking tests share: a one-module program read from text, whose
//! declarations are checked one at a time.

use std::sync::Arc;

use model::ModuleId;
use model::semantic_types::Types;
use project::ImportKind;
use source::SourceFile;

use crate::Checker;

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
    }
}

/// A diagnostic as its code, message and the text of its first label.
pub(crate) type Reported = (&'static str, String, String);

/// Runs `work` on a checker of the module `app:main` written as `text`, which
/// imports `app:utils` as `utils` (an import added after `text`, so the
/// places in `text` stay as written), which declares `Shown` and the private
/// `hidden`. Gives what `work` gives, the program's
/// types and the diagnostics.
pub(crate) fn with_checker<T>(
    text: &str,
    work: impl FnOnce(&mut Checker) -> T,
) -> (T, Types, Vec<Reported>) {
    let (utils_id, main_id) = (ModuleId::from_index(0), ModuleId::from_index(1));
    let imports = vec![project::Import {
        kind: ImportKind::Declared(0),
        module: utils_id,
    }];
    let text = format!("{text}\nimport \"app:utils\";\n");
    let modules = [
        module("app:utils", "fn Shown() {}\nfn hidden() {}", Vec::new()),
        module("app:main", &text, imports),
    ];
    let file = modules[1].ast.file.clone();
    let mut checker = Checker::new(&modules, 64);
    let result = checker.in_module(main_id, work);
    let reported = checker
        .diagnostics
        .iter()
        .flatten()
        .map(|diagnostic| {
            let text = file.slice(diagnostic.labels[0].span).to_string();
            (diagnostic.code.id, diagnostic.message.clone(), text)
        })
        .collect();
    (result, checker.types, reported)
}

/// `expected` as `Reported`s, to compare with what was reported.
pub(crate) fn reported(expected: &[(&'static str, &str, &str)]) -> Vec<Reported> {
    let owned = |&(code, message, text): &(&'static str, &str, &str)| {
        (code, message.to_string(), text.to_string())
    };
    expected.iter().map(owned).collect()
}
