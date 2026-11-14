//! Reading raw pointers. The parser's tree dump is its own tests', so these
//! look at the tree.

use arena::Id;
use syntax::ast;

use super::*;
use crate::testing::Tree;
use crate::testing::parse_text;
use crate::testing::statements;

/// The written type `ty` as `*` for each pointer to a pointer, then
/// `rawptr` or the innermost type's text.
fn shape(tree: &Tree, ty: Id<ast::Type<Peeper>>) -> String {
    match tree.types[ty] {
        ast::Type::Feature(FeatureType::Pointer(PointerType {
            target: Some(target),
        })) => format!("*{}", shape(tree, target)),
        ast::Type::Feature(FeatureType::Pointer(PointerType { target: None })) => {
            "rawptr".to_string()
        }
        ast::Type::Reference { target, .. } => format!("&{}", shape(tree, target)),
        _ => tree.file.slice(tree.types.span(ty)).to_string(),
    }
}

/// The types of the parameters of the first function of `tree`.
fn parameter_types(tree: &Tree) -> Vec<String> {
    let decl = tree.declarations().next().unwrap();
    let ast::Decl::Function(function) = tree.decls[decl] else {
        panic!("not a function")
    };
    (tree.children.lists.items(function.signature.parameters))
        .map(|parameter| shape(tree, tree.children.params[parameter].ty))
        .collect()
}

/// A pointer's target is any type, a pointer or `rawptr` too, and a
/// pointer is any type's part.
#[test]
fn parses_types() {
    let (tree, errors) = parse_text("fn f(a: *u8, b: **u8, c: *rawptr, d: rawptr, e: &*rawptr);");
    assert!(errors.is_empty(), "{errors:?}");
    assert_eq!(
        parameter_types(&tree),
        ["*u8", "**u8", "*rawptr", "rawptr", "&*rawptr"]
    );
}

/// A spelling that can only be a type is read as one where a value could
/// go, for an argument that takes a type; `@` takes the place after it.
#[test]
fn parses_expressions() {
    let (tree, errors) = parse_text("fn f() { g(*Node, rawptr); @a.b; }");
    assert!(errors.is_empty(), "{errors:?}");
    let found = statements(&tree);
    let ast::Expr::Call { arguments, .. } = tree.exprs[found[0]] else {
        panic!("not a call: {:?}", tree.exprs[found[0]])
    };
    let types: Vec<String> = (tree.children.lists.items(arguments))
        .map(|argument| match tree.exprs[argument] {
            ast::Expr::Type(ty) => shape(&tree, ty),
            other => panic!("not a type: {other:?}"),
        })
        .collect();
    assert_eq!(types, ["*Node", "rawptr"]);
    let ast::Expr::Feature(FeatureExpr::RawAddress(RawAddress { place })) = tree.exprs[found[1]]
    else {
        panic!("not a raw address: {:?}", tree.exprs[found[1]])
    };
    assert_eq!(tree.file.slice(tree.exprs.span(place)), "a.b");
}

/// Every span of a tree read from broken pointers lies in the text and
/// starts before it ends.
#[test]
fn spans_lie_in_the_text() {
    for text in [
        "fn f(a: *",
        "fn f(a: *) {}",
        "fn f() { @ }",
        "fn f() { g(*); }",
    ] {
        let (tree, _) = parse_text(text);
        let spans = (tree.types.ids().map(|id| tree.types.span(id)))
            .chain(tree.exprs.ids().map(|id| tree.exprs.span(id)))
            .chain(tree.decls.ids().map(|id| tree.decls.span(id)));
        for span in spans {
            let (start, end) = (span.start as usize, span.end as usize);
            assert!(start <= end && end <= text.len(), "{text:?}: {span:?}");
        }
    }
}
