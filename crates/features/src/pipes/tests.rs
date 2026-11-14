//! Reading pipes, and their refusal.

use arena::Id;
use syntax::ast;

use super::*;
use crate::testing::Tree;
use crate::testing::bodies;
use crate::testing::messages;
use crate::testing::parse_text;
use crate::testing::statements;

/// `expr` with each pipe in parentheses.
fn grouped(tree: &Tree, expr: Id<ast::Expr<Peeper>>) -> String {
    match tree.exprs[expr] {
        ast::Expr::Feature(FeatureExpr::Pipe(Pipe { value, call })) => {
            format!("({} |> {})", grouped(tree, value), grouped(tree, call))
        }
        _ => tree.file.slice(tree.exprs.span(expr)).to_string(),
    }
}

/// A pipe groups to the left and binds looser than `+`.
#[test]
fn parses_pipes() {
    let (tree, errors) = parse_text("fn f() { a |> f(b) |> g(); a + b |> f(); }");
    assert!(errors.is_empty(), "{errors:?}");
    let found: Vec<String> = (statements(&tree).into_iter())
        .map(|statement| grouped(&tree, statement))
        .collect();
    assert_eq!(found, ["((a |> f(b)) |> g())", "(a + b |> f())"]);
}

/// A pipe's target is a call of a free function.
#[test]
fn target_errors() {
    assert_eq!(
        messages("fn f() { a |> b; }"),
        ["pipe target must be a free-function call"]
    );
    assert_eq!(
        messages("fn f() { a |> b.c(); }"),
        ["pipe cannot call a method"]
    );
}

/// A pipe is not checked yet, so it is refused where it is written.
#[test]
fn refused() {
    let (_, reported) = bodies("fn g(a: i32) {}\nfn f(a: i32) { a |> g(); }");
    let found: Vec<(&str, &str)> = (reported.iter())
        .map(|(_, message, text)| (message.as_str(), text.as_str()))
        .collect();
    assert_eq!(found, [("pipes are not supported yet", "a |> g()")]);
}
