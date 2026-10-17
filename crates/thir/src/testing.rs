//! What the checking tests share: a one-module program read from text.

use std::collections::HashMap;
use std::sync::Arc;

use ir::types::Types;
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
}

/// A diagnostic as its code, message and the text of its first label.
pub(crate) type Reported = (&'static str, String, String);

/// Runs `check` on the module `app:main` written as `text`, which imports
/// `app:utils` as `utils`. Gives what `check` gives, the program's types and
/// the diagnostics.
pub(crate) fn check<T>(
    text: &str,
    check: impl FnOnce(&mut Checker<Program>) -> T,
) -> (T, Types, Vec<Reported>) {
    let file = Arc::new(SourceFile::new("main.peep", text));
    let (syntax, parse) = parser::parse(&file);
    assert!(parse.is_empty(), "{parse:?}");
    let imports = vec![project::Import {
        path: "app:utils".to_string(),
        alias: Some("utils".to_string()),
        span: source::Span::new(0, 0),
    }];
    let module = project::Module {
        path: "app:main".to_string(),
        syntax,
        imports,
        diagnostics: Vec::new(),
    };
    let indexes = HashMap::from([("app:utils", 0), ("app:main", 1)]);
    let mut diagnostics = Vec::new();
    let scope = collect(1, &module, &indexes, &mut diagnostics);
    let mut program = Program {
        types: Types::new(),
    };
    let mut checker = Checker {
        module: &module,
        scope: &scope,
        queries: &mut program,
        diagnostics: &mut diagnostics,
    };
    let result = check(&mut checker);
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
