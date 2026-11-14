//! Reading and checking ranges. The parser's tree dump and the checker's
//! test program are those crates' own, so these build what they need.

use arena::Id;
use syntax::ast;
use syntax::ast::WrittenRange;

use super::*;
use crate::optionals::Fallback;
use crate::optionals::OrElse;
use crate::testing::Tree;
use crate::testing::bodies;
use crate::testing::parse_text;

/// The expression `id` with each range and binary operation in
/// parentheses, and the rest as written.
fn grouped(tree: &Tree, id: Id<ast::Expr<Peeper>>) -> String {
    let text = |span| tree.file.slice(span).to_string();
    match tree.exprs[id] {
        ast::Expr::Feature(FeatureExpr::Range(WrittenRange {
            inclusive,
            start,
            end,
            step,
        })) => {
            let part = |part: Option<Id<ast::Expr<Peeper>>>| {
                part.map_or(String::new(), |part| grouped(tree, part))
            };
            let op = if inclusive { "..=" } else { ".." };
            let step = step.map_or(String::new(), |step| format!(":{}", grouped(tree, step)));
            format!("({}{op}{}{step})", part(start), part(end))
        }
        ast::Expr::Binary { op, left, right } => {
            let (left, right) = (grouped(tree, left), grouped(tree, right));
            format!("({left} {} {right})", op.symbol())
        }
        ast::Expr::Feature(FeatureExpr::OrElse(OrElse {
            value,
            fallback: Fallback::Value(fallback),
        })) => {
            format!(
                "({} orelse {})",
                grouped(tree, value),
                grouped(tree, fallback)
            )
        }
        ast::Expr::Index { value, index } => {
            format!("{}[{}]", grouped(tree, value), grouped(tree, index))
        }
        _ => text(tree.exprs.span(id)),
    }
}

/// A range binds looser than every binary operator and tighter than
/// `orelse`, its step is read as an end is, and ranges group to the left.
#[test]
fn precedence() {
    for (text, want) in [
        ("x.y.z[0][i..j]", "x.y.z[0][(i..j)]"),
        ("a + b..c * d", "((a + b)..(c * d))"),
        ("a orelse b..c", "(a orelse (b..c))"),
        ("a..b orelse c", "(a..(b orelse c))"),
        ("a..=b:c + d", "(a..=b:(c + d))"),
        ("a..b..c", "((a..b)..c)"),
        ("a[..b]", "a[(..b)]"),
        ("a[b..]", "a[(b..)]"),
    ] {
        let (tree, errors) = parse_text(&format!("fn f() {{ {text}; }}"));
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

/// Only right before a `]` may a range leave out its end, and an inclusive
/// one never.
#[test]
fn open_ends() {
    for (text, message) in [
        (
            "fn f() { a[1..=]; }",
            "inclusive range requires an end bound",
        ),
        (
            "fn f() { for i in 0.. { } }",
            "expected expression, found `{`",
        ),
    ] {
        let (_, errors) = parse_text(text);
        let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
        assert_eq!(got, [message], "{text}");
    }
}

/// A range's literal start takes the end's type; its variables are
/// declared in the body. Its step is the ends' type made unsigned, 1
/// when not written.
#[test]
fn loops() {
    let (bodies, reported) = bodies(
        "fn f(n: i64) {
                for i in 0..n { let j = i; }
                for 0..3 { }
                for k in 1..=n { }
                for i, m in n..0:2 { }
            }",
    );
    assert!(reported.is_empty(), "{reported:?}");
    assert_eq!(
        bodies,
        ["    for i.1 in 0:i64..n.0 by 1:u64 {
        let j.2 = i.1
    }
    for 0:i32..3:i32 by 1:u32 {
    }
    for k.3 in 1:i64..=n.0 by 1:u64 {
    }
    for i.4, m.5 in n.0..0:i64 by 2:u64 {
    }
"]
    );
}
