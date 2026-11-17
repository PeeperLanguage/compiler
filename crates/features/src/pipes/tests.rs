//! Reading pipes, and checking them as calls.

use arena::Id;
use syntax::ast;

use crate::testing::Tree;
use crate::testing::bodies;
use crate::testing::parse_text;
use crate::testing::statements;

/// `expr` with each pipe in parentheses.
fn grouped(tree: &Tree, expr: Id<ast::Expr>) -> String {
    match tree.exprs[expr] {
        ast::Expr::Feature(ast::FeatureExpr::Pipe(ast::Pipe { value, target })) => {
            format!("({} |> {})", grouped(tree, value), grouped(tree, target))
        }
        _ => tree.file.slice(tree.exprs.span(expr)).to_string(),
    }
}

/// A pipe groups to the left and binds looser than `+`; what is written
/// after its target, as `as`, takes the whole pipe. Any target is read:
/// the checker says what one may be.
#[test]
fn parses_pipes() {
    let (tree, errors) =
        parse_text("fn f() { a |> f(b) |> g(); a + b |> f(); a |> b; c |> len() as i32; }");
    assert!(errors.is_empty(), "{errors:?}");
    let found: Vec<String> = (statements(&tree).into_iter())
        .map(|statement| grouped(&tree, statement))
        .collect();
    assert_eq!(
        found[..3],
        ["((a |> f(b)) |> g())", "(a + b |> f())", "(a |> b)"]
    );
    let last = (tree.exprs.ids())
        .filter(|&id| {
            matches!(
                tree.exprs[id],
                ast::Expr::Feature(ast::FeatureExpr::Pipe(_))
            )
        })
        .last()
        .unwrap();
    assert_eq!(tree.file.slice(tree.exprs.span(last)), "c |> len()");
}

/// A pipe is checked as the call of its target with the value first.
#[test]
fn checked_as_calls() {
    let (found, reported) = bodies("fn g(a: i32, b: i32) {}\nfn f(a: i32) { a |> g(2); }");
    assert!(reported.is_empty(), "{reported:?}");
    assert!(found[1].contains("(a.0, 2:i32)"), "{found:?}");
    let (_, reported) = bodies(
        "fn g(a: i32) -> i32 { return a; }\nfn f(a: bool) { a |> g(); 1 |> g(2); 1 |> g; 1 |> g().x; }",
    );
    let found: Vec<(&str, &str)> = (reported.iter())
        .map(|(_, message, text)| (message.as_str(), text.as_str()))
        .collect();
    assert_eq!(
        found,
        [
            ("mismatched types: expected `i32`, found `bool`", "a"),
            ("`g` takes 1 argument, but 2 were given", "1 |> g(2)"),
            ("a pipe's target must be a call", "g"),
            ("a pipe's target must be a call", "g().x"),
        ]
    );
}
