//! Reading and checking string literals. The parser's tree dump and the
//! checker's test program are their own crates' tests, so these build what
//! they need.

use std::convert::Infallible;
use std::sync::Arc;

use ::check::Checked;
use ::check::Checker;
use ::check::builder::ThirBuilder;
use arena::Id;
use arena::List;
use model::ConstArguments;
use model::DefId;
use model::FunctionCopy;
use model::ModuleId;
use model::machine::Machine;
use model::semantic_types::TypeId;
use model::thir;
use model::thir::Body;
use parser::Grammar;
use parser::Rules;
use source::SourceFile;
use source::Span;
use syntax::ast;
use syntax::ast::Language;
use syntax::ast::StringKind;
use syntax::operators::Precedence;

use super::*;

/// The language the text tests are written in: the core and text.
#[derive(Clone, Copy, Debug)]
enum Texts {}

impl Language for Texts {
    type Expr = StringLiteral;
    type Type = Infallible;

    fn precedence(_: &StringLiteral) -> Precedence {
        Precedence::Call
    }

    fn fields(ty: &Infallible) -> Option<List<ast::Field<Texts>>> {
        match *ty {}
    }

    fn methods(ty: &Infallible) -> Option<List<ast::Decl<Texts>>> {
        match *ty {}
    }

    fn pointee(ty: &Infallible) -> Option<Id<ast::Type<Texts>>> {
        match *ty {}
    }

    fn element(ty: &Infallible) -> Option<Id<ast::Type<Texts>>> {
        match *ty {}
    }

    fn string_kind(expr: &StringLiteral) -> Option<StringKind> {
        Some(expr.kind)
    }

    fn written_range(_: &StringLiteral) -> Option<ast::WrittenRange<Texts>> {
        None
    }
}

static RULES: Rules<Texts> = {
    let mut rules = Rules::core();
    parse::rows(&mut rules);
    rules
};

impl Grammar for Texts {
    fn rules() -> &'static Rules<Texts> {
        &RULES
    }
}

impl Checked for Texts {
    fn check_expr(
        builder: &mut ThirBuilder<'_, '_, Texts>,
        expr: StringLiteral,
        span: Span,
        _: Option<TypeId>,
    ) -> thir::Expr {
        crate::check::check_literal(builder, expr, span)
    }

    fn resolve_type(_: &mut Checker<'_, Texts>, ty: Infallible, _: Span) -> TypeId {
        match ty {}
    }

    fn declare_type(
        _: &mut Checker<'_, Texts>,
        _: DefId,
        _: ast::TypeDecl<Texts>,
        ty: Infallible,
    ) -> TypeId {
        match ty {}
    }
}

/// A diagnostic as its code, message and the text of its first label.
type Reported = (&'static str, String, String);

/// Runs `work` on a checker of the one module `app:main`, written as
/// `text`, given the functions' declarations in order; gives what `work`
/// gives, the body texts it built and the diagnostics.
fn with_checker<T>(
    text: &str,
    work: impl FnOnce(&mut Checker<Texts>, &[(DefId, Span)]) -> T,
) -> (T, Vec<Reported>) {
    let file = Arc::new(SourceFile::new("main.peep", text));
    let (ast, parse) = parser::parse::<Texts>(&file);
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

/// `"..."` is a plain string literal and `c"..."` a C one, each spanning
/// its quotes.
#[test]
fn parses_literals() {
    let text = r#"fn f() { g("a", c"b"); }"#;
    let (tree, errors) = parser::parse::<Texts>(&Arc::new(SourceFile::new("t.peep", text)));
    assert!(errors.is_empty(), "{errors:?}");
    let literals: Vec<(StringKind, &str)> = (tree.exprs.ids())
        .filter_map(|id| match tree.exprs[id] {
            ast::Expr::Feature(StringLiteral { kind }) => {
                Some((kind, tree.file.slice(tree.exprs.span(id))))
            }
            _ => None,
        })
        .collect();
    assert_eq!(
        literals,
        [(StringKind::Plain, "\"a\""), (StringKind::C, "c\"b\"")]
    );
}

/// An `extern` function is C's, so it has no body here, and any other
/// function has one; their calls are not lowered.
#[test]
fn only_an_extern_function_has_no_body() {
    let text = "#[extern(\"f\")]\nfn f(a: i8) -> i8 { return a; }\nfn g();\n";
    let (found, reported) = with_checker(text, |checker, functions| {
        (functions.iter())
            .map(|&(definition, name)| checker.function_type(definition, name))
            .collect::<Vec<_>>()
    });
    let body = "a function needs a body, or `#[extern(\"name\")]` when C defines it";
    assert_eq!(
        reported,
        [
            (
                "P0005",
                "an `extern` function is defined in C, so it has no body".to_string(),
                "f".to_string()
            ),
            ("P0005", body.to_string(), "g".to_string()),
        ]
    );
    assert_eq!(found, [TypeId::INVALID, TypeId::INVALID]);
}

/// A C string a compile-time run gives back is its text, read up to the
/// zero byte that ends it, as C reads it.
#[test]
fn a_run_gives_back_a_c_string_without_its_end() {
    let text = "fn hello() -> &cstr { return c\"hello\"; }
        fn g() {
            const Greeting = hello();
            let greeting = Greeting;
        }";
    let (built, reported) = with_checker(text, |checker, functions| {
        let (definition, name) = functions[1];
        let mut body = Body::default();
        let copy = FunctionCopy {
            definition,
            arguments: ConstArguments::NONE,
        };
        assert!(checker.check_body(copy, name, &mut body, true));
        let file = SourceFile::new("main.peep", text);
        body.text(&checker.types, &file, |_| "hello")
    });
    assert!(reported.is_empty(), "{reported:?}");
    assert_eq!(built, "    let greeting.0 = c\"hello\"\n");
}
