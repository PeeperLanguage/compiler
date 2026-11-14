//! Reading character literals, and their refusal.

use syntax::ast;

use super::*;
use crate::testing::bodies;
use crate::testing::parse_text;

/// `'x'` is a character and `b'y'` a byte, each spanning its quotes.
#[test]
fn parses_literals() {
    let (tree, errors) = parse_text("fn f() { 'x'; b'y'; }");
    assert!(errors.is_empty(), "{errors:?}");
    let literals: Vec<(bool, &str)> = (tree.exprs.ids())
        .filter_map(|id| match tree.exprs[id] {
            ast::Expr::Feature(FeatureExpr::Char(CharLiteral { byte })) => {
                Some((byte, tree.file.slice(tree.exprs.span(id))))
            }
            _ => None,
        })
        .collect();
    assert_eq!(literals, [(false, "'x'"), (true, "b'y'")]);
}

/// A character has no type yet, so a literal is refused where it is
/// written.
#[test]
fn refused() {
    let (_, reported) = bodies("fn f() { 'x'; }");
    let found: Vec<(&str, &str)> = (reported.iter())
        .map(|(_, message, text)| (message.as_str(), text.as_str()))
        .collect();
    assert_eq!(found, [("character literals are not supported yet", "'x'")]);
}
