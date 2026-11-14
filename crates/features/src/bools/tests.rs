//! Reading bools. The parser's tree dump is its own tests', so these look
//! at the tree.

use std::sync::Arc;

use source::SourceFile;
use syntax::ast;

use super::*;

/// `true` and `false` are bool literals, each spanning its word, and an
/// operand like any value.
#[test]
fn parses_literals() {
    let text = "fn f() { true; false; !true == false; }";
    let (tree, errors) = parser::parse::<Peeper>(&Arc::new(SourceFile::new("t.peep", text)));
    assert!(errors.is_empty(), "{errors:?}");
    let literals: Vec<(bool, &str)> = (tree.exprs.ids())
        .filter_map(|id| match tree.exprs[id] {
            ast::Expr::Feature(FeatureExpr::Bool(BoolLiteral { value })) => {
                Some((value, tree.file.slice(tree.exprs.span(id))))
            }
            _ => None,
        })
        .collect();
    assert_eq!(
        literals,
        [
            (true, "true"),
            (false, "false"),
            (true, "true"),
            (false, "false")
        ]
    );
}
