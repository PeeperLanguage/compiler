//! Optional types and `orelse`: the parser reads `?T` and `value orelse
//! fallback`. The checker refuses both until optional values exist.

use arena::Id;
use token::TokenKind;
use tree::ast;
use tree::operators;

use crate::Parser;
use crate::Rules;

/// Adds the rules that read optional types and `orelse` to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.type_start(TokenKind::Question, optional_type);
    rules.type_start(TokenKind::QuestionQuestion, doubled_optional_type);
    rules.infix(TokenKind::Orelse, operators::Precedence::OrElse, or_else);
}

/// Reads the type after a `?` that starts at `start`.
fn optional_type(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let inner = p.type_expr();
    p.add(start, ast::Type::Optional(inner))
}

/// Reads the type after a `??`, one token holding two markers. The second
/// changes nothing (`??T` is `?T`); it is kept in the tree as written and
/// left to a lint to report.
fn doubled_optional_type(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let inner = optional_type(p, start + 1);
    p.add(start, ast::Type::Optional(inner))
}

/// Reads `orelse fallback` or `orelse { block }` after `value`. The fallback
/// may itself end in `orelse`: a orelse b orelse c is a orelse (b orelse c).
fn or_else(p: &mut Parser, value: Id<ast::Expr>) -> Id<ast::Expr> {
    p.next();
    let fallback = if p.at(TokenKind::LeftBrace) {
        ast::OrElseFallback::Block(p.block())
    } else {
        // One below OrElse: groups to the right.
        ast::OrElseFallback::Value(p.expr(operators::Precedence::Lowest))
    };
    p.add(p.start_of(value), ast::Expr::OrElse { value, fallback })
}

#[cfg(test)]
mod tests {
    //! Reading optional types and `orelse`.

    use crate::testing;
    use arena::Id;
    use tree::ast;

    /// `expr` with each `orelse` in parentheses; a block fallback as `{}`.
    fn grouped(tree: &ast::Tree, expr: Id<ast::Expr>) -> String {
        match tree.exprs[expr] {
            ast::Expr::OrElse { value, fallback } => {
                let fallback = match fallback {
                    ast::OrElseFallback::Value(fallback) => grouped(tree, fallback),
                    ast::OrElseFallback::Block(_) => "{}".to_string(),
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
        let text = "fn f() { a orelse b orelse c; a || b orelse { return; }; d orelse e..f; g..h orelse i; }";
        let (tree, errors) = testing::parse_text(text);
        assert!(errors.is_empty(), "{errors:?}");
        let found: Vec<String> = (testing::statements(&tree).into_iter())
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
            .filter(|&id| matches!(tree.exprs[id], ast::Expr::OrElse { .. }))
            .last()
            .unwrap();
        assert_eq!(tree.file.slice(tree.exprs.span(last)), "h orelse i");
    }

    /// Each `?` is one optional type, and `??` is two; a missing fallback is
    /// one error.
    #[test]
    fn parses_types() {
        let (tree, errors) = testing::parse_text("fn f(a: ?i32, b: ??i32, c: ? ??u8);");
        assert!(errors.is_empty(), "{errors:?}");
        let optionals = (tree.types.ids())
            .filter(|&id| matches!(tree.types[id], ast::Type::Optional(_)))
            .map(|id| tree.file.slice(tree.types.span(id)))
            .collect::<Vec<_>>();
        assert_eq!(optionals.len(), 6, "{optionals:?}");
        assert!(optionals.contains(&"?i32") && optionals.contains(&"??i32"));
        assert_eq!(testing::messages("fn f() { a orelse ; }").len(), 1);
    }

    /// Nesting far deeper than the thread's stack would hold still parses.
    #[test]
    fn deep_nesting_does_not_overflow() {
        let n = 100_000;
        for text in [
            format!("fn f() {{ let a = b{}", " orelse { c".repeat(n)),
            format!("fn f(a: {}i32) {{}}", "?".repeat(n)),
        ] {
            let (tree, _) = testing::parse_text(&text);
            assert_eq!(tree.top_level.len(), 1);
        }
    }
}
