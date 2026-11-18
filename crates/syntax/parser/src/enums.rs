//! Enums: the type `enum { A, B: Payload }` and its declaration, a variant
//! literal (`Enum::Case with payload`), a case test (`x is Enum::Case`) and
//! `match`.

use crate::declaration;
use crate::is_name;
use std::num;
use tree::ast;
use tree::operators;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use token::TokenKind;

use crate::Parser;
use crate::Rules;

/// Adds the rules that read enum types and declarations, variant
/// literals, `is` tests and `match` to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.declaration(TokenKind::Enum, declaration::type_decl);
    rules.type_start(TokenKind::Enum, enum_type);
    rules.infix(
        TokenKind::With,
        operators::Precedence::Call,
        variant_literal,
    );
    rules.infix(TokenKind::Is, operators::Precedence::Equality, case_test);
    rules.statement(TokenKind::Match, match_statement);
}

/// Reads `{ Name, Name: Payload, ... }`, the variants of an enum type that
/// starts at `start`.
fn enum_type(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let variants = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        true,
        is_name,
        |p| {
            let start = p.tok().span.start;
            let name = p.name();
            let payload = p.at(TokenKind::Colon).then(|| {
                p.next();
                p.type_expr()
            });
            p.add(start, ast::Variant { name, payload })
        },
    );
    p.add(start, ast::Type::Enum { variants })
}

/// Reports `case` unless it is a full path `Enum::Case` (or a type already
/// reported as invalid).
fn require_path(p: &mut Parser, case: Id<ast::Type>) {
    if !matches!(p.tree.types[case], ast::Type::Path(_) | ast::Type::Invalid) {
        let span = p.tree.types.span(case);
        p.repair(
            Diagnostic::new(
                diagnostics::INVALID_EXPRESSION,
                "enum case must use a fully named path",
            )
            .label(&p.tree.file, span, "write `Enum::Variant`"),
        );
    }
}

/// Reads `with payload` after `case`, which is a full path `Enum::Case`.
fn variant_literal(p: &mut Parser, case: Id<ast::Expr>) -> Id<ast::Expr> {
    p.next();
    let payload = p.expr(operators::Precedence::Lowest);
    let path = match p.tree.exprs[case] {
        ast::Expr::Path(path) if matches!(p.tree.types[path], ast::Type::Path(_)) => path,
        _ => {
            let span = p.tree.exprs.span(case);
            p.repair(
                Diagnostic::new(
                    diagnostics::INVALID_EXPRESSION,
                    "enum case must use a fully named path",
                )
                .label(&p.tree.file, span, "write `Enum::Variant`"),
            );
            return case;
        }
    };
    p.add(
        p.start_of(case),
        ast::Expr::VariantLiteral {
            case: path,
            payload,
        },
    )
}

/// Reads `is Enum::Case` after `value`; the case is a full path.
fn case_test(p: &mut Parser, value: Id<ast::Expr>) -> Id<ast::Expr> {
    p.next();
    let case = p.type_expr();
    require_path(p, case);
    p.add(p.start_of(value), ast::Expr::CaseTest { value, case })
}

/// Reads `match subject { arms }`. An arm is `_ => { }`, `Enum::Case => { }`
/// or `Enum::Case with pattern => { }`; arms are not separated by commas.
fn match_statement(p: &mut Parser) -> Id<ast::Stmt> {
    let start = p.next().span.start;
    let subject = p.expr(operators::Precedence::Lowest);
    let mut arms = List::EMPTY;
    if let Some(open) = p.expect(TokenKind::LeftBrace) {
        let mut read: Vec<num::NonZeroU32> = Vec::new();
        // Every arm begins with `_` or a path and reads at least it, so the
        // loop always moves on.
        while p.at(TokenKind::Underscore) || p.at(TokenKind::Ident) {
            let arm_start = p.tok().span.start;
            let (pattern, body) = match_arm(p);
            read.push(p.add(arm_start, ast::MatchArm { pattern, body }).into());
            let t = p.tok();
            if t.kind == TokenKind::Comma {
                p.next();
                let fix = p.fix(t.span, "");
                p.repair(
                    Diagnostic::new(diagnostics::EXPECTED_TOKEN, "match arms do not use commas")
                        .label(&p.tree.file, t.span, "")
                        .help("remove this comma", vec![fix]),
                );
            }
        }
        arms = p.tree.children.lists.add(&read);
        p.close(TokenKind::RightBrace, open);
    }
    p.add(start, ast::Stmt::Match { subject, arms })
}

/// Reads one arm's pattern, its `=>` and its block. A missing `=>` before
/// the block is read as if it were there.
fn match_arm(p: &mut Parser) -> (Id<ast::Pattern>, Id<ast::Block>) {
    let t = p.tok();
    let pattern = if t.kind == TokenKind::Underscore {
        let start = p.next().span.start;
        p.add(start, ast::Pattern::Wildcard)
    } else {
        case_pattern(p)
    };
    if p.at(TokenKind::LeftBrace) {
        let at = p.here();
        let fix = p.fix(at, " =>");
        p.repair(
            Diagnostic::new(
                diagnostics::EXPECTED_TOKEN,
                "expected `=>` after match case",
            )
            .label(&p.tree.file, at, "")
            .help("add missing `=>`", vec![fix]),
        );
    } else if p.expect(TokenKind::FatArrow).is_none() {
        p.sync();
    }
    (pattern, p.block())
}

/// Reads `Enum::Case`, perhaps followed by `with` and the pattern of its
/// payload. The case is a full path.
fn case_pattern(p: &mut Parser) -> Id<ast::Pattern> {
    // Cases in cases, `A::B with C::D with ...`, are a deep recursion.
    source::grow_stack(|| {
        let start = p.tok().span.start;
        let case = p.type_expr();
        require_path(p, case);
        let inner = p.at(TokenKind::With).then(|| {
            p.next();
            payload_pattern(p)
        });
        p.add(start, ast::Pattern::Case { case, inner })
    })
}

/// Reads the pattern after `with`: `none`, `_`, a name to bind,
/// `.{ fields }`, or another case.
fn payload_pattern(p: &mut Parser) -> Id<ast::Pattern> {
    let t = p.tok();
    match t.kind {
        TokenKind::None => {
            p.next();
            p.add(t.span.start, ast::Pattern::None)
        }
        TokenKind::Underscore => {
            p.next();
            p.add(t.span.start, ast::Pattern::Wildcard)
        }
        TokenKind::Dot => p.struct_pattern(),
        TokenKind::Ident if !matches!(p.ahead(1).kind, TokenKind::ColonColon | TokenKind::Less) => {
            let name = p.name();
            p.tree.add(ast::Pattern::Binding, name.span)
        }
        TokenKind::Ident => case_pattern(p),
        _ => {
            p.fail(
                Diagnostic::new(
                    diagnostics::EXPECTED_TOKEN,
                    "expected a pattern after `with`",
                )
                .label(&p.tree.file, t.span, "")
                .help(
                    "write `none`, `_`, a name, `.{ field }`, or a case of the payload's enum",
                    Vec::new(),
                ),
            );
            p.add(p.here().start, ast::Pattern::Invalid)
        }
    }
}

#[cfg(test)]
mod tests {
    //! Reading enums and `match`, and their errors.

    use crate::testing;
    use arena::Id;
    use tree::ast;

    /// The pattern `pattern` as written, a case's payload pattern in
    /// parentheses.
    fn shape(tree: &ast::Tree, pattern: Id<ast::Pattern>) -> String {
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
            ast::Pattern::Binding => {
                format!("bind {}", tree.file.slice(tree.patterns.span(pattern)))
            }
            ast::Pattern::Struct(fields) => format!("struct of {}", fields.len()),
            other => panic!("not a match pattern: {other:?}"),
        }
    }

    /// An enum's variants are names, each perhaps with a payload type; a
    /// trailing or doubled comma is style.
    #[test]
    fn parses_declarations() {
        let (tree, errors) =
            testing::parse_text("enum Status { Ready, Waiting: Pair<i32>,, Done, }");
        assert!(errors.is_empty(), "{errors:?}");
        let decl = tree.declarations().next().unwrap();
        let ast::Decl::Type(declared) = tree.decls[decl] else {
            panic!("not a type declaration")
        };
        let ast::Type::Enum { variants } = tree.types[declared.ty] else {
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
        assert_eq!(testing::messages("enum E { A B }").len(), 1);
        assert_eq!(testing::messages("enum E { A: }").len(), 1);
    }

    /// A variant literal takes the whole expression after `with`, and a case
    /// test binds as `==` does.
    #[test]
    fn parses_expressions() {
        let (tree, errors) =
            testing::parse_text("fn f() { Shape::Line with 1 + 2; a is E::B && c; }");
        assert!(errors.is_empty(), "{errors:?}");
        let found = testing::statements(&tree);
        let text = |span| tree.file.slice(span);
        let ast::Expr::VariantLiteral { case, payload } = tree.exprs[found[0]] else {
            panic!("not a variant literal: {:?}", tree.exprs[found[0]])
        };
        assert_eq!(text(tree.types.span(case)), "Shape::Line");
        assert_eq!(text(tree.exprs.span(payload)), "1 + 2");
        let ast::Expr::Binary { left, .. } = tree.exprs[found[1]] else {
            panic!("not a binary operation: {:?}", tree.exprs[found[1]])
        };
        let ast::Expr::CaseTest { value, case } = tree.exprs[left] else {
            panic!("not a case test: {:?}", tree.exprs[left])
        };
        assert_eq!(text(tree.exprs.span(value)), "a");
        assert_eq!(text(tree.types.span(case)), "E::B");
    }

    /// An arm is `_`, a case, or a case with the pattern of its payload:
    /// `none`, `_`, a name, a struct pattern or another case.
    #[test]
    fn parses_matches() {
        let (tree, errors) = testing::parse_text(
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
                ast::Stmt::Match { arms, .. } => Some(arms),
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
                testing::messages(&format!("fn f() {{ {text} }}")),
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
            let (tree, errors) = testing::parse_text(text);
            assert_eq!(tree.top_level.len(), 1, "{text}");
            assert!(!errors.is_empty(), "{text}");
        }
        let deep = format!(
            "fn f() {{ match x {{ {}c => {{}} }} }}",
            "A::B with ".repeat(100_000)
        );
        assert_eq!(testing::parse_text(&deep).0.top_level.len(), 1);
    }
}
