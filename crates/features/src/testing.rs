//! What the features' tests share: a text read as Peeper, and a checker of
//! a one-module program.

use std::sync::Arc;

use arena::Id;
use check::Checker;
use diagnostics::Diagnostic;
use model::ConstArguments;
use model::DeclarationCopy;
use model::DefId;
use model::ModuleId;
use model::machine::Machine;
use model::thir::Body;
use source::SourceFile;
use source::Span;
use syntax::ast;

use crate::Peeper;

pub(crate) type Tree = ast::Tree<Peeper>;

/// The tree of `text` and its syntax errors.
pub(crate) fn parse_text(text: &str) -> (Tree, Vec<Diagnostic>) {
    parser::parse(&Arc::new(SourceFile::new("t.peep", text)))
}

/// The message of each syntax error of `text`.
pub(crate) fn messages(text: &str) -> Vec<String> {
    let (_, errors) = parse_text(text);
    errors.into_iter().map(|error| error.message).collect()
}

/// A diagnostic as its code, message and the text of its first label.
pub(crate) type Reported = (&'static str, String, String);

/// Runs `work` on a checker of the one module `app:main`, written as
/// `text`, given the functions' declarations in order; gives what `work`
/// gives and the diagnostics.
pub(crate) fn with_checker<T>(
    text: &str,
    work: impl FnOnce(&mut Checker<Peeper>, &[(DefId, Span)]) -> T,
) -> (T, Vec<Reported>) {
    let file = Arc::new(SourceFile::new("main.peep", text));
    let (ast, parse) = parser::parse::<Peeper>(&file);
    assert!(parse.is_empty(), "{parse:?}");
    let modules = [model::program::Module {
        path: "app:main".to_string(),
        ast,
        imports: Vec::new(),
    }];
    let tree = &modules[0].ast;
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

/// The expressions of the statements of the first function of `tree`.
pub(crate) fn statements(tree: &Tree) -> Vec<Id<ast::Expr<Peeper>>> {
    let decl = tree.declarations().next().unwrap();
    let ast::Decl::Function(function) = tree.decls[decl] else {
        panic!("not a function")
    };
    let statements = tree.blocks[function.body.unwrap()].statements;
    let expression = |statement| match tree.stmts[statement] {
        ast::Stmt::Expression(expr) => expr,
        other => panic!("not an expression statement: {other:?}"),
    };
    tree.children
        .lists
        .items(statements)
        .map(expression)
        .collect()
}

/// The text of the body of every function of `text`, a called function
/// named by its place among them (`f0`).
pub(crate) fn bodies(text: &str) -> (Vec<String>, Vec<Reported>) {
    let file = SourceFile::new("main.peep", text);
    with_checker(text, |checker, functions| {
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
                let mut body = Body::default();
                assert!(checker.check_body(copy, span, &mut body, true));
                body.text(&checker.types, &file, name)
            })
            .collect()
    })
}
