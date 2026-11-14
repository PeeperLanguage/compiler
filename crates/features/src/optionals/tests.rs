//! Reading optional types and `orelse`, and their refusal.

use arena::Id;
use syntax::ast;

use super::*;
use crate::testing::Tree;
use crate::testing::bodies;
use crate::testing::messages;
use crate::testing::parse_text;
use crate::testing::statements;

/// `expr` with each `orelse` in parentheses; a block fallback as `{}`.
fn grouped(tree: &Tree, expr: Id<ast::Expr<Peeper>>) -> String {
    match tree.exprs[expr] {
        ast::Expr::Feature(FeatureExpr::OrElse(OrElse { value, fallback })) => {
            let fallback = match fallback {
                Fallback::Value(fallback) => grouped(tree, fallback),
                Fallback::Block(_) => "{}".to_string(),
            };
            format!("({} orelse {fallback})", grouped(tree, value))
        }
        _ => tree.file.slice(tree.exprs.span(expr)).to_string(),
    }
}

/// `orelse` groups to the right and binds looser than `||` and `..`; its
/// fallback may be a block.
#[test]
fn parses_or_else() {
    let text =
        "fn f() { a orelse b orelse c; a || b orelse { return; }; d orelse e..f; g..h orelse i; }";
    let (tree, errors) = parse_text(text);
    assert!(errors.is_empty(), "{errors:?}");
    let found: Vec<String> = (statements(&tree).into_iter())
        .map(|statement| grouped(&tree, statement))
        .collect();
    assert_eq!(
        found,
        [
            "(a orelse (b orelse c))",
            "(a || b orelse {})",
            "(d orelse e..f)",
            "g..h orelse i"
        ]
    );
    // The last one is a range whose end is the `orelse`.
    let last = (tree.exprs.ids())
        .filter(|&id| matches!(tree.exprs[id], ast::Expr::Feature(FeatureExpr::OrElse(_))))
        .last()
        .unwrap();
    assert_eq!(tree.file.slice(tree.exprs.span(last)), "h orelse i");
}

/// Each `?` is one optional type, and `??` is two; a missing fallback is
/// one error.
#[test]
fn parses_types() {
    let (tree, errors) = parse_text("fn f(a: ?i32, b: ??i32, c: ? ??u8);");
    assert!(errors.is_empty(), "{errors:?}");
    let optionals = (tree.types.ids())
        .filter(|&id| matches!(tree.types[id], ast::Type::Feature(FeatureType::Optional(_))))
        .map(|id| tree.file.slice(tree.types.span(id)))
        .collect::<Vec<_>>();
    assert_eq!(optionals.len(), 6, "{optionals:?}");
    assert!(optionals.contains(&"?i32") && optionals.contains(&"??i32"));
    assert_eq!(messages("fn f() { a orelse ; }").len(), 1);
}

/// Nesting far deeper than the thread's stack would hold still parses.
#[test]
fn deep_nesting_does_not_overflow() {
    let n = 100_000;
    for text in [
        format!("fn f() {{ let a = b{}", " orelse { c".repeat(n)),
        format!("fn f(a: {}i32) {{}}", "?".repeat(n)),
    ] {
        let (tree, _) = parse_text(&text);
        assert_eq!(tree.top_level.len(), 1);
    }
}

/// Neither is checked yet, so each is refused where it is written.
#[test]
fn refused() {
    let (_, reported) = bodies("fn f(a: ?i32) {}\nfn g(b: i32) { b orelse 1; }");
    let found: Vec<(&str, &str)> = (reported.iter())
        .map(|(_, message, text)| (message.as_str(), text.as_str()))
        .collect();
    assert_eq!(
        found,
        [
            ("optional types are not supported yet", "?i32"),
            ("`orelse` fallbacks are not supported yet", "b orelse 1"),
        ]
    );
}
