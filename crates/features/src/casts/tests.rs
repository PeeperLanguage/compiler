//! Reading casts. The parser's tree dump is its own tests', so these look
//! at the tree.

use std::sync::Arc;

use arena::Id;
use source::SourceFile;
use syntax::ast;

use super::*;
use crate::testing::Tree;

/// The expression `id` with each cast and operation that holds a cast in
/// parentheses, and the rest as written.
fn grouped(tree: &Tree, id: Id<ast::Expr<Peeper>>) -> String {
    let text = |span| tree.file.slice(span).to_string();
    match tree.exprs[id] {
        ast::Expr::Feature(FeatureExpr::Cast(Cast { value, ty })) => {
            format!(
                "({} as {})",
                grouped(tree, value),
                text(tree.types.span(ty))
            )
        }
        ast::Expr::Binary { op, left, right } => {
            format!(
                "({} {} {})",
                grouped(tree, left),
                op.symbol(),
                grouped(tree, right)
            )
        }
        _ => text(tree.exprs.span(id)),
    }
}

/// A cast binds tighter than every binary operator.
#[test]
fn precedence() {
    for (text, want) in [
        ("a + b as i64", "(a + (b as i64))"),
        ("a as i32 * b", "((a as i32) * b)"),
    ] {
        let source = Arc::new(SourceFile::new("t.peep", format!("fn f() {{ {text}; }}")));
        let (tree, errors) = parser::parse::<Peeper>(&source);
        assert!(errors.is_empty(), "{text}: {errors:?}");
        let decl = tree.declarations().next().unwrap();
        let ast::Decl::Function(function) = tree.decls[decl] else {
            panic!("not a function")
        };
        let statements = tree.blocks[function.body.unwrap()].statements;
        let statement = tree.children.lists.items(statements).next().unwrap();
        let ast::Stmt::Expression(expr) = tree.stmts[statement] else {
            panic!("not an expression statement")
        };
        assert_eq!(grouped(&tree, expr), want, "{text}");
    }
}
