//! What the checking tests share: a one-module program read from text, whose
//! declarations are checked one at a time.

use std::sync::Arc;

use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::ModuleId;
use model::machine::Machine;
use model::program::ImportKind;
use model::semantic_types::Types;
use model::thir;
use source::SourceFile;
use source::Span;
use tree::ast;

use crate::Checker;

/// The module `path`, written as `text`, with `imports`; its text must parse
/// without errors.
pub(crate) fn module(
    path: &str,
    text: &str,
    imports: Vec<model::program::Import>,
) -> model::program::Module {
    let file = Arc::new(SourceFile::new("main.peep", text));
    let (ast, parse) = parser::parse(&file);
    assert!(parse.is_empty(), "{parse:?}");
    model::program::Module {
        path: path.to_string(),
        ast,
        imports,
    }
}

/// A diagnostic as its code, message and the text of its first label.
pub(crate) type Reported = (&'static str, String, String);

/// Runs `work` on a checker of the module `app:main` written as `text`, given
/// its id, which
/// imports `app:utils` as `utils` (an import added after `text`, so the
/// places in `text` stay as written), which declares `Shown` and the private
/// `hidden`. Gives what `work` gives, the program's
/// types and the diagnostics.
pub(crate) fn with_checker<T>(
    text: &str,
    work: impl FnOnce(&mut Checker, ModuleId) -> T,
) -> (T, Types, Vec<Reported>) {
    let (utils_id, main_id) = (ModuleId::from_index(0), ModuleId::from_index(1));
    let imports = vec![model::program::Import {
        kind: ImportKind::Declared(0),
        module: utils_id,
    }];
    let text = format!("{text}\nimport \"app:utils\";\n");
    let modules = [
        module("app:utils", "fn Shown() {}\nfn hidden() {}", Vec::new()),
        module("app:main", &text, imports),
    ];
    let file = modules[1].ast.file.clone();
    let mut checker = Checker::new(&modules, Machine::named("x86_64-linux").unwrap(), false);
    let result = work(&mut checker, main_id);
    let reported = checker
        .module_checks
        .iter()
        .flat_map(|module| &module.diagnostics)
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

/// Runs `work` on a checker of the one module `app:main`, written as
/// `text`, given the functions' declarations in order; gives what `work`
/// gives and the diagnostics.
pub(crate) fn with_functions<T>(
    text: &str,
    work: impl FnOnce(&mut Checker, &[(DefId, Span)]) -> T,
) -> (T, Vec<Reported>) {
    let modules = [module("app:main", text, Vec::new())];
    let tree = &modules[0].ast;
    let file = tree.file.clone();
    let functions: Vec<(DefId, Span)> = (tree.declarations().enumerate())
        .filter_map(|(index, declaration)| match tree.decls[declaration] {
            ast::Decl::Function(function) => {
                let module = ModuleId::from_index(0);
                let index = index as u32;
                Some((DefId { module, index }, function.name.span))
            }
            _ => None,
        })
        .collect();
    let mut checker = Checker::new(&modules, Machine::named("x86_64-linux").unwrap(), false);
    let result = work(&mut checker, &functions);
    let reported = (checker.module_checks.iter())
        .flat_map(|module| &module.diagnostics)
        .map(|diagnostic| {
            let label = file.slice(diagnostic.labels[0].span).to_string();
            (diagnostic.code.id, diagnostic.message.clone(), label)
        })
        .collect();
    (result, reported)
}

/// The text of the body of every function of `text`, a called function
/// named by its place among them (`f0`).
pub(crate) fn bodies(text: &str) -> (Vec<String>, Vec<Reported>) {
    let file = SourceFile::new("main.peep", text);
    with_functions(text, |checker, functions| {
        let names: Vec<String> = (0..functions.len())
            .map(|index| format!("f{index}"))
            .collect();
        let name = |function: DefId| names[function.index as usize].as_str();
        (functions.iter())
            .map(|&(definition, span)| {
                let copy = DeclarationCopy {
                    definition,
                    arguments: ConstArguments::NONE,
                };
                let mut body = thir::Body::default();
                assert!(checker.check_body(copy, span, &mut body, true));
                body.text(&checker.types, &file, name)
            })
            .collect()
    })
}
