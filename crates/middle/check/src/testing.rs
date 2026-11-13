//! What the checking tests share: a one-module program read from text, whose
//! declarations are checked one at a time.

use arena::List;
use model::DefId;
use model::semantic_types::TypeId;
use model::thir;
use parser::Grammar;
use parser::Rules;
use source::Span;
use std::convert::Infallible;
use std::sync::Arc;
use syntax::ast;
use syntax::ast::Language;
use syntax::operators::Precedence;

use model::ModuleId;
use model::machine::Machine;
use model::program::ImportKind;
use model::semantic_types::Types;
use source::SourceFile;

use crate::Checked;
use crate::Checker;
use crate::builder::ThirBuilder;

/// The language the checking tests are written in: the core, with no
/// feature, since each feature's crate depends on this one.
#[derive(Clone, Copy, Debug)]
pub(crate) enum Bare {}

impl Language for Bare {
    type Expr = Infallible;
    type Type = Infallible;

    fn precedence(expr: &Infallible) -> Precedence {
        match *expr {}
    }

    fn fields(ty: &Infallible) -> Option<List<ast::Field<Bare>>> {
        match *ty {}
    }

    fn methods(ty: &Infallible) -> Option<List<ast::Decl<Bare>>> {
        match *ty {}
    }
}

static RULES: Rules<Bare> = Rules::core();

impl Grammar for Bare {
    fn rules() -> &'static Rules<Bare> {
        &RULES
    }
}

impl Checked for Bare {
    fn check_expr(
        _: &mut ThirBuilder<'_, '_, Bare>,
        expr: Infallible,
        _: Span,
        _: Option<TypeId>,
    ) -> thir::Expr {
        match expr {}
    }

    fn resolve_type(_: &mut Checker<'_, Bare>, ty: Infallible, _: Span) -> TypeId {
        match ty {}
    }

    fn declare_type(
        _: &mut Checker<'_, Bare>,
        _: DefId,
        _: ast::TypeDecl<Bare>,
        ty: Infallible,
    ) -> TypeId {
        match ty {}
    }
}

/// The module `path`, written as `text`, with `imports`; its text must parse
/// without errors.
pub(crate) fn module(
    path: &str,
    text: &str,
    imports: Vec<model::program::Import>,
) -> model::program::Module<Bare> {
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
    work: impl FnOnce(&mut Checker<Bare>, ModuleId) -> T,
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
