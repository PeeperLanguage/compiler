//! Raw pointers: the parser reads `rawptr`, `*T` and `@x`. The checker gives
//! them their types and addresses.

use arena::Id;
use token::TokenKind;
use tree::ast;
use tree::operators;

use crate::Parser;
use crate::Rules;

/// Adds the rules that read raw pointer types and `@place` to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.type_start(TokenKind::Asterisk, typed_pointer);
    rules.type_start(TokenKind::Rawptr, raw_pointer);
    // types that cannot be values
    rules.prefix(TokenKind::Asterisk, type_value);
    rules.prefix(TokenKind::Rawptr, type_value);
    rules.prefix(TokenKind::At, raw_address);
}

/// Reads the `T` of `*T`, a pointer type that starts at `start`.
fn typed_pointer(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let target = Some(p.type_expr());
    p.add(start, ast::Type::Pointer { target })
}

/// `rawptr`, which starts at `start`.
fn raw_pointer(p: &mut Parser, start: u32) -> Id<ast::Type> {
    p.add(start, ast::Type::Pointer { target: None })
}

/// Reads a type that cannot be a value (`*T`, `rawptr`) where a value could
/// go.
fn type_value(p: &mut Parser) -> Id<ast::Expr> {
    let start = p.tok().span.start;
    let ty = p.type_expr();
    p.add(start, ast::Expr::Type(ty))
}

/// Reads `@x`.
fn raw_address(p: &mut Parser) -> Id<ast::Expr> {
    let start = p.next().span.start;
    let place = p.expr(operators::Precedence::Prefix);
    p.add(start, ast::Expr::RawAddress(place))
}

#[cfg(test)]
mod tests {
    //! Reading raw pointers.

    use crate::testing;
    use arena::Id;
    use tree::ast;

    /// The written type `ty` as `*` for each pointer to a pointer, then
    /// `rawptr` or the innermost type's text.
    fn shape(tree: &ast::Tree, ty: Id<ast::Type>) -> String {
        match tree.types[ty] {
            ast::Type::Pointer {
                target: Some(target),
            } => format!("*{}", shape(tree, target)),
            ast::Type::Pointer { target: None } => "rawptr".to_string(),
            ast::Type::Reference { target, .. } => format!("&{}", shape(tree, target)),
            _ => tree.file.slice(tree.types.span(ty)).to_string(),
        }
    }

    /// The types of the parameters of the first function of `tree`.
    fn parameter_types(tree: &ast::Tree) -> Vec<String> {
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
        let (tree, errors) =
            testing::parse_text("fn f(a: *u8, b: **u8, c: *rawptr, d: rawptr, e: &*rawptr);");
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
        let (tree, errors) = testing::parse_text("fn f() { g(*Node, rawptr); @a.b; }");
        assert!(errors.is_empty(), "{errors:?}");
        let found = testing::statements(&tree);
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
        let ast::Expr::RawAddress(place) = tree.exprs[found[1]] else {
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
            let (tree, _) = testing::parse_text(text);
            let spans = (tree.types.ids().map(|id| tree.types.span(id)))
                .chain(tree.exprs.ids().map(|id| tree.exprs.span(id)))
                .chain(tree.decls.ids().map(|id| tree.decls.span(id)));
            for span in spans {
                let (start, end) = (span.start as usize, span.end as usize);
                assert!(start <= end && end <= text.len(), "{text:?}: {span:?}");
            }
        }
    }
}
