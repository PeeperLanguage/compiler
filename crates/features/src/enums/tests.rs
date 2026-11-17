//! Reading enums and `match`, and their refusal.

use arena::Id;
use syntax::ast;

use crate::testing::Tree;
use crate::testing::bodies;
use crate::testing::messages;
use crate::testing::parse_text;
use crate::testing::statements;

/// The pattern `pattern` as written, a case's payload pattern in
/// parentheses.
fn shape(tree: &Tree, pattern: Id<ast::Pattern>) -> String {
    match tree.patterns[pattern] {
        ast::Pattern::Case { case, inner } => {
            let case = tree.file.slice(tree.types.span(case));
            match inner {
                Some(inner) => format!("{case}({})", shape(tree, inner)),
                None => case.to_string(),
            }
        }
        ast::Pattern::Wildcard => "_".to_string(),
        ast::Pattern::None => "none".to_string(),
        ast::Pattern::Binding => format!("bind {}", tree.file.slice(tree.patterns.span(pattern))),
        ast::Pattern::Struct(fields) => format!("struct of {}", fields.len()),
        other => panic!("not a match pattern: {other:?}"),
    }
}

/// An enum's variants are names, each perhaps with a payload type; a
/// trailing or doubled comma is style.
#[test]
fn parses_declarations() {
    let (tree, errors) = parse_text("enum Status { Ready, Waiting: Pair<i32>,, Done, }");
    assert!(errors.is_empty(), "{errors:?}");
    let decl = tree.declarations().next().unwrap();
    let ast::Decl::Type(declared) = tree.decls[decl] else {
        panic!("not a type declaration")
    };
    let ast::Type::Feature(ast::FeatureType::Enum(ast::EnumType { variants })) =
        tree.types[declared.ty]
    else {
        panic!("not an enum")
    };
    let found: Vec<(&str, Option<&str>)> = (tree.children.lists.items(variants))
        .map(|variant| {
            let variant = tree.children.variants[variant];
            let payload = variant
                .payload
                .map(|ty| tree.file.slice(tree.types.span(ty)));
            (tree.file.slice(variant.name.span), payload)
        })
        .collect();
    assert_eq!(
        found,
        [
            ("Ready", None),
            ("Waiting", Some("Pair<i32>")),
            ("Done", None)
        ]
    );
    assert_eq!(messages("enum E { A B }").len(), 1);
    assert_eq!(messages("enum E { A: }").len(), 1);
}

/// A variant literal takes the whole expression after `with`, and a case
/// test binds as `==` does.
#[test]
fn parses_expressions() {
    let (tree, errors) = parse_text("fn f() { Shape::Line with 1 + 2; a is E::B && c; }");
    assert!(errors.is_empty(), "{errors:?}");
    let found = statements(&tree);
    let text = |span| tree.file.slice(span);
    let ast::Expr::Feature(ast::FeatureExpr::VariantLiteral(literal)) = tree.exprs[found[0]] else {
        panic!("not a variant literal: {:?}", tree.exprs[found[0]])
    };
    assert_eq!(text(tree.types.span(literal.case)), "Shape::Line");
    assert_eq!(text(tree.exprs.span(literal.payload)), "1 + 2");
    let ast::Expr::Binary { left, .. } = tree.exprs[found[1]] else {
        panic!("not a binary operation: {:?}", tree.exprs[found[1]])
    };
    let ast::Expr::Feature(ast::FeatureExpr::CaseTest(test)) = tree.exprs[left] else {
        panic!("not a case test: {:?}", tree.exprs[left])
    };
    assert_eq!(text(tree.exprs.span(test.value)), "a");
    assert_eq!(text(tree.types.span(test.case)), "E::B");
}

/// An arm is `_`, a case, or a case with the pattern of its payload:
/// `none`, `_`, a name, a struct pattern or another case.
#[test]
fn parses_matches() {
    let (tree, errors) = parse_text(
        "fn f() {
            match shape {
                Shape::Dot => { }
                Shape::Line with .{ end } => { }
                Shape::Maybe with none => { }
                Shape::Wrap with Inner::Leaf with value => { }
                Shape::Any with _ => { }
                _ => { }
            }
        }",
    );
    assert!(errors.is_empty(), "{errors:?}");
    let arms = (tree.stmts.ids())
        .find_map(|id| match tree.stmts[id] {
            ast::Stmt::Feature(ast::FeatureStmt::Match(ast::Match { arms, .. })) => Some(arms),
            _ => None,
        })
        .unwrap();
    let found: Vec<String> = (tree.children.lists.items(arms))
        .map(|arm| shape(&tree, tree.children.match_arms[arm].pattern))
        .collect();
    assert_eq!(
        found,
        [
            "Shape::Dot",
            "Shape::Line(struct of 1)",
            "Shape::Maybe(none)",
            "Shape::Wrap(Inner::Leaf(bind value))",
            "Shape::Any(_)",
            "_"
        ]
    );
}

/// Each mistake is one error.
#[test]
fn errors() {
    let path = "enum case must use a fully named path";
    for (text, message) in [
        ("a is b;", path),
        ("a with 1;", path),
        ("match x { a => {} }", path),
        ("match x { A::B {} }", "expected `=>` after match case"),
        (
            "match x { A::B => {}, _ => {} }",
            "match arms do not use commas",
        ),
        (
            "match x { A::B with => {} }",
            "expected a pattern after `with`",
        ),
    ] {
        assert_eq!(
            messages(&format!("fn f() {{ {text} }}")),
            [message],
            "{text}"
        );
    }
}

/// Broken input still gives a tree, and nesting far deeper than the
/// thread's stack would hold still parses.
#[test]
fn broken_and_deep_input() {
    for text in [
        "fn f() { A::B with ; }",
        "fn f() { match }",
        "fn f() { match x { }",
        "fn f() { match x { let } }",
    ] {
        let (tree, errors) = parse_text(text);
        assert_eq!(tree.top_level.len(), 1, "{text}");
        assert!(!errors.is_empty(), "{text}");
    }
    let deep = format!(
        "fn f() {{ match x {{ {}c => {{}} }} }}",
        "A::B with ".repeat(100_000)
    );
    assert_eq!(parse_text(&deep).0.top_level.len(), 1);
}

/// Nothing of an enum is checked yet, so each part is refused where it is
/// written.
#[test]
fn refused() {
    let (_, reported) = bodies(
        "enum E { A, B: i32 }
         fn f(e: E) {}
         fn g(a: i32) { E::B with a; }
         fn h(a: i32) { a is E::A; }
         fn i(a: i32) { match a { _ => { } } }",
    );
    let found: Vec<&str> = (reported.iter())
        .map(|(_, message, _)| message.as_str())
        .collect();
    assert_eq!(
        found,
        [
            "enum declarations are not supported yet",
            "variant literals are not supported yet",
            "`is` tests are not supported yet",
            "`match` statements are not supported yet",
        ]
    );
}
