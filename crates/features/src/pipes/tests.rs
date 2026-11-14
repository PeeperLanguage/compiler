//! Reading pipes as calls, and checking them.

use arena::Id;
use syntax::ast;

use super::*;
use crate::testing::Tree;
use crate::testing::bodies;
use crate::testing::messages;
use crate::testing::parse_text;
use crate::testing::statements;

/// `expr` with each call as `callee(arguments)`, its callee as written.
fn calls(tree: &Tree, expr: Id<ast::Expr<Peeper>>) -> String {
    match tree.exprs[expr] {
        ast::Expr::Call { callee, arguments } => {
            let arguments: Vec<String> = (tree.children.lists.items(arguments))
                .map(|argument| calls(tree, argument))
                .collect();
            let callee = tree.file.slice(tree.exprs.span(callee));
            format!("{callee}({})", arguments.join(", "))
        }
        _ => tree.file.slice(tree.exprs.span(expr)).to_string(),
    }
}

/// The piped value is the first written argument, of a function or of a
/// method, after its `self`. A pipe groups to the left and binds looser
/// than `+`; what is written after its call, as `as`, takes the whole pipe.
#[test]
fn parses_pipes_as_calls() {
    let (tree, errors) =
        parse_text("fn f() { a |> f(b) |> g(); a + b |> f(); c |> x.m(d); c |> len() as i32; }");
    assert!(errors.is_empty(), "{errors:?}");
    let found: Vec<String> = (statements(&tree).into_iter())
        .map(|statement| calls(&tree, statement))
        .collect();
    assert_eq!(found[..3], ["g(f(a, b))", "f(a + b)", "x.m(c, d)"]);
    let piped = tree.exprs.ids().find(|&id| calls(&tree, id) == "len(c)");
    assert_eq!(
        tree.file.slice(tree.exprs.span(piped.unwrap())),
        "c |> len()"
    );
}

#[test]
fn target_errors() {
    assert_eq!(
        messages("fn f() { a |> b; }"),
        ["expected a call after `|>`"]
    );
}

/// A pipe is checked as the call it is.
#[test]
fn checked_as_calls() {
    let (found, reported) = bodies("fn g(a: i32, b: i32) {}\nfn f(a: i32) { a |> g(2); }");
    assert!(reported.is_empty(), "{reported:?}");
    assert!(found[1].contains("(a.0, 2:i32)"), "{found:?}");
    let (_, reported) = bodies("fn g(a: i32) {}\nfn f(a: bool) { a |> g(); 1 |> g(2); }");
    let found: Vec<(&str, &str)> = (reported.iter())
        .map(|(_, message, text)| (message.as_str(), text.as_str()))
        .collect();
    assert_eq!(
        found,
        [
            ("mismatched types: expected `i32`, found `bool`", "a"),
            ("`g` takes 1 argument, but 2 were given", "1 |> g(2)"),
        ]
    );
}
