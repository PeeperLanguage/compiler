//! Reading panics. What a panic does when it runs is the test programs'
//! to show.

use syntax::ast;

use super::*;
use crate::testing::bodies;
use crate::testing::messages;
use crate::testing::parse_text;

/// `panic` takes one expression and ends with `;`.
#[test]
fn parses_panics() {
    let (tree, errors) = parse_text("fn f() { panic \"stop\"; panic (message); }");
    assert!(errors.is_empty(), "{errors:?}");
    let found: Vec<&str> = (tree.stmts.ids())
        .filter_map(|id| match tree.stmts[id] {
            ast::Stmt::Feature(FeatureStmt::Panic(Panic { message })) => {
                Some(tree.file.slice(tree.exprs.span(message)))
            }
            _ => None,
        })
        .collect();
    assert_eq!(found, ["\"stop\"", "(message)"]);
    assert_eq!(messages("fn f() { panic ; }").len(), 1);
}

/// The message is a plain string literal, in parentheses or not; any
/// other value is refused, once.
#[test]
fn message_is_a_string_literal() {
    let (bodies, reported) = bodies(
        "fn f(a: i32) { panic (\"stop\"); }
         fn g(a: i32) { panic a; }
         fn h() { panic c\"stop\"; }
         fn i() { panic missing; }",
    );
    assert_eq!(bodies[0], "    panic \"stop\"\n");
    let found: Vec<(&str, &str)> = (reported.iter())
        .map(|(_, message, text)| (message.as_str(), text.as_str()))
        .collect();
    let refused = "`panic` messages other than string literals are not supported yet";
    assert_eq!(found.len(), 3, "{found:?}");
    assert_eq!(found[0], (refused, "a"));
    assert_eq!(found[1], (refused, "c\"stop\""));
    assert_eq!(found[2].1, "missing");
}
