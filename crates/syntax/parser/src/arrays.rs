//! Arrays: `[N]T` and the array literals `[N]T{ ... }` and `[_]T{ ... }`.
//! A literal's entries may spread (`values...`), which a call to a `...`
//! parameter takes too.

use crate::Parser;
use crate::Rules;
use crate::expression;
use arena::Id;
use diagnostics::Diagnostic;
use source::Span;
use token::Token;
use token::TokenKind;
use tree::ast;

/// Adds the rules that read array types and array literals to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.type_start(TokenKind::LeftBracket, array_type);
    rules.prefix(TokenKind::LeftBracket, array_literal);
}

/// Reads the `N]T` of `[N]T`, a type that starts at `start`, with its `[`;
/// a `[]T` alone is reported, as it has no fixed size.
fn array_type(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let open = Token {
        kind: TokenKind::LeftBracket,
        span: Span {
            start,
            end: start + 1,
        },
    };
    let t = p.tok();
    // `[]T` alone, read whole so the message names it and parsing goes on.
    if t.kind == TokenKind::RightBracket {
        p.next();
        let element = p.type_expr();
        let ty = p.add(start, ast::Type::Invalid);
        // An element reported already, as `[]i32` in `[][]i32`, is the
        // one mistake: its fix fixes the whole.
        if let ast::Type::Invalid = p.tree.types[element] {
            return ty;
        }
        let element = p.tree.file.slice(p.tree.types.span(element));
        let message =
            format!("`[]{element}` is any number of `{element}`s, so it has no fixed size");
        let label = format!("use `[N]{element}` for a fixed array or `&[]{element}` for a slice");
        let span = p.tree.types.span(ty);
        p.repair(Diagnostic::new(diagnostics::INVALID_TYPE, message).label(
            &p.tree.file,
            span,
            label,
        ));
        return ty;
    }
    if t.kind != TokenKind::Number {
        p.fail(
            Diagnostic::new(diagnostics::INVALID_TYPE, "expected array length").label(
                &p.tree.file,
                t.span,
                "use `[N]T` for fixed arrays or `&[]T` for slices",
            ),
        );
        return p.add(start, ast::Type::Invalid);
    }
    p.next();
    p.check_number(t);
    p.close(TokenKind::RightBracket, open);
    let element = p.type_expr();
    let length = t.span;
    p.add(start, ast::Type::Array { length, element })
}

/// Reads `[N]T{ entries }` or `[_]T{ entries }` (the length is the number of
/// values they give); an entry is a value, or `values...`, which spreads an
/// array or a range: `(0..3)...` or `0..3...`. Without `{` after it, `[N]T`
/// is the type alone.
fn array_literal(p: &mut Parser) -> Id<ast::Expr> {
    let start = p.tok().span.start;
    let ty = if p.ahead(1).kind == TokenKind::Underscore {
        let open = p.next();
        p.next();
        p.close(TokenKind::RightBracket, open);
        let element = p.type_expr();
        ast::ArrayLiteralType::Counted { element }
    } else {
        let ty = p.type_expr();
        // Without entries `[N]T` is the type alone, as an argument that
        // takes one; `[_]T` needs its entries to have a length.
        if !p.at(TokenKind::LeftBrace) {
            return p.add(start, ast::Expr::Type(ty));
        }
        ast::ArrayLiteralType::Written(ty)
    };
    let entries = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        false,
        expression::starts_expression,
        expression::spreadable,
    );
    p.add(start, ast::Expr::ArrayLiteral { ty, entries })
}

#[cfg(test)]
mod tests {
    //! Parsing arrays: the types `[N]T`, the literals `[N]T{ ... }` and
    //! `[_]T{ ... }`, and their errors.

    use crate::testing;
    use arena::Id;
    use tree::ast;

    /// The written type `ty` with `[N]` for each array, `&[]` for each slice
    /// and the innermost type's text.
    fn shape(tree: &ast::Tree, ty: Id<ast::Type>) -> String {
        match tree.types[ty] {
            ast::Type::Array { length, element } => {
                format!("[{}]{}", tree.file.slice(length), shape(tree, element))
            }
            ast::Type::Slice { element, .. } => format!("&[]{}", shape(tree, element)),
            _ => tree.file.slice(tree.types.span(ty)).to_string(),
        }
    }

    /// `[N]T` is a type wherever a type goes, an array's element too, and
    /// where a value could go when no `{` follows; `[N]T{ }` and `[_]T{ }` are
    /// literals, whose entries may spread.
    #[test]
    fn parses_types_and_literals() {
        let text = "fn f(a: [4]u8, b: [2][3]i32, c: &[][2]u8) {
        g([4]i32);
        [2]i32{ 1, 2 };
        [_]u8{ 1, (b)..., a... };
    }";
        let (tree, errors) = testing::parse_text(text);
        assert!(errors.is_empty(), "{errors:?}");
        let decl = tree.declarations().next().unwrap();
        let ast::Decl::Function(function) = tree.decls[decl] else {
            panic!("not a function")
        };
        let parameters: Vec<String> = (tree.children.lists.items(function.signature.parameters))
            .map(|parameter| shape(&tree, tree.children.params[parameter].ty))
            .collect();
        assert_eq!(parameters, ["[4]u8", "[2][3]i32", "&[][2]u8"]);
        let mut literals = Vec::new();
        for id in tree.exprs.ids() {
            match tree.exprs[id] {
                ast::Expr::Type(ty) => literals.push(format!("type {}", shape(&tree, ty))),
                ast::Expr::ArrayLiteral { ty, entries } => {
                    let ty = match ty {
                        ast::ArrayLiteralType::Written(ty) => shape(&tree, ty),
                        ast::ArrayLiteralType::Counted { element } => {
                            format!("[_]{}", shape(&tree, element))
                        }
                    };
                    let entries: Vec<&str> = (tree.children.lists.items(entries))
                        .map(|entry| match tree.exprs[entry] {
                            ast::Expr::Spread(_) => "spread",
                            _ => "value",
                        })
                        .collect();
                    literals.push(format!("{ty} {}", entries.join(" ")));
                }
                _ => {}
            }
        }
        assert_eq!(
            literals,
            [
                "type [4]i32",
                "[2]i32 value value",
                "[_]u8 value spread spread"
            ]
        );
    }

    /// A `[]T` alone has no fixed size, and is read whole, so the parser stays
    /// in step and reports the next one too; an element so reported is the
    /// one mistake.
    #[test]
    fn type_errors() {
        for (text, want) in [
            ("fn f(a: [n]i32) {}", &["expected array length"][..]),
            (
                "fn f(a: []i32) {}",
                &["`[]i32` is any number of `i32`s, so it has no fixed size"],
            ),
            (
                "fn f(a: [][]i32) {}",
                &["`[]i32` is any number of `i32`s, so it has no fixed size"],
            ),
            (
                "fn f(a: []i32, b: []u8) {}",
                &[
                    "`[]i32` is any number of `i32`s, so it has no fixed size",
                    "`[]u8` is any number of `u8`s, so it has no fixed size",
                ],
            ),
            ("fn f() { [x]i32{}; }", &["expected array length"]),
            (
                "fn f() { []i32{}; }",
                &["`[]i32` is any number of `i32`s, so it has no fixed size"],
            ),
            ("fn f() { [_]i32; }", &["expected `{`, found `;`"]),
        ] {
            assert_eq!(testing::messages(text), want, "{text}");
        }
    }

    /// A `;` for a `,` in a literal's `{ }` is repaired with one error per
    /// list; a `;` after an unclosed literal ends the statement, so its `}` is
    /// missing.
    #[test]
    fn separators() {
        let text = "fn f() { let v = [2]i32{1; 2}; }";
        let (_, errors) = testing::parse_text(text);
        assert_eq!(errors.len(), 1, "{errors:?}");
        assert_eq!(errors[0].message, "expected `,` between items, found `;`");
        let mut fixed = text.to_string();
        for fix in errors[0].notes[0].fixes().iter().rev() {
            fixed.replace_range(fix.span.range(), &fix.new_text);
        }
        assert_eq!(fixed, "fn f() { let v = [2]i32{1, 2}; }");
        assert_eq!(
            testing::messages("fn f() {\n    let v = [3]i32{4, 5, 6;\n    return;\n}"),
            ["expected `}`, found `;`"]
        );
    }
}
