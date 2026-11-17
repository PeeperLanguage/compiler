//! Reading `print` and `println`. What they accept is the test programs'
//! to show.

use syntax::ast;

use crate::testing::messages;
use crate::testing::parse_text;

/// Each takes any number of values, none too; `println` is the one with a
/// newline.
#[test]
fn parses_prints() {
    let (tree, errors) = parse_text("fn f() { print(); println(items[n], 1); }");
    assert!(errors.is_empty(), "{errors:?}");
    let prints: Vec<(bool, usize)> = (tree.exprs.ids())
        .filter_map(|id| match tree.exprs[id] {
            ast::Expr::Feature(ast::FeatureExpr::Print(ast::Print { newline, values })) => {
                Some((newline, values.len()))
            }
            _ => None,
        })
        .collect();
    assert_eq!(prints, [(false, 0), (true, 2)]);
}

/// An unclosed list is one error, and the statement after it is read.
#[test]
fn unclosed_values() {
    assert_eq!(
        messages("fn f() { print(; return; }"),
        ["expected `)`, found `;`"]
    );
}
