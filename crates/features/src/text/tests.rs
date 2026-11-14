//! Reading and checking string literals. The parser's tree dump and the checker's
//! test program are those crates' own, so these build what they need.

use std::sync::Arc;

use model::ConstArguments;
use model::FunctionCopy;
use model::semantic_types::TypeId;
use model::thir::Body;
use source::SourceFile;
use syntax::ast;
use syntax::ast::StringKind;

use super::*;
use crate::testing::with_checker;

/// `"..."` is a plain string literal and `c"..."` a C one, each spanning
/// its quotes.
#[test]
fn parses_literals() {
    let text = r#"fn f() { g("a", c"b"); }"#;
    let (tree, errors) = parser::parse::<Peeper>(&Arc::new(SourceFile::new("t.peep", text)));
    assert!(errors.is_empty(), "{errors:?}");
    let literals: Vec<(StringKind, &str)> = (tree.exprs.ids())
        .filter_map(|id| match tree.exprs[id] {
            ast::Expr::Feature(FeatureExpr::String(StringLiteral { kind })) => {
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
