//! Structs: `struct { name: Type, ... }` and `Type.{ name = value, ... }`.
//! The tree holds a struct type (`ast::Type::Struct`) and a struct literal
//! (`ast::Expr::StructLiteral`), with `ast::Field` and `ast::FieldValue`.
//! Reading a field, as in `point.x`, is an infix rule of `expression.rs`.

use crate::declaration;
use crate::is_name;
use arena::Id;
use diagnostics::Diagnostic;
use token::TokenKind;
use tree::ast;
use tree::operators;

use crate::Parser;
use crate::Rules;

/// Adds the rules that read struct types, declarations and literals
/// to `rules`.
pub(crate) const fn rows(rules: &mut Rules) {
    rules.declaration(TokenKind::Struct, declaration::type_decl);
    rules.type_start(TokenKind::Struct, struct_type);
    rules.prefix(TokenKind::Dot, anonymous_literal);
    rules.after_path(TokenKind::Dot, TokenKind::LeftBrace, typed_literal);
}

/// Reads `{ name: Type, ... }`, the fields of a struct type that starts at
/// `start`.
fn struct_type(p: &mut Parser, start: u32) -> Id<ast::Type> {
    let fields = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        true,
        is_name,
        |p| {
            let start = p.tok().span.start;
            let name = p.name();
            p.expect(TokenKind::Colon);
            let ty = p.type_expr();
            p.add(start, ast::Field { name, ty })
        },
    );
    p.add(start, ast::Type::Struct { fields })
}

/// Reads `.{ name = value, ... }`, a struct literal without a type.
fn anonymous_literal(p: &mut Parser) -> Id<ast::Expr> {
    let start = p.tok().span.start;
    literal(p, None, start)
}

/// Reads `.{ name = value, ... }` after the type `path`.
fn typed_literal(p: &mut Parser, path: Id<ast::Type>) -> Id<ast::Expr> {
    let start = p.tree.types.span(path).start;
    literal(p, Some(path), start)
}

/// Reads `.{ name = value, ... }` after `ty` (none when there is no type), a
/// literal that starts at `start`. A `name: value` field, the colon of a
/// declaration where a field is set, is read as if it were `=`, and all such
/// colons are reported in one error.
fn literal(p: &mut Parser, ty: Option<Id<ast::Type>>, start: u32) -> Id<ast::Expr> {
    p.next(); // `.`
    let mut colons = Vec::new();
    let fields = p.delimited(
        TokenKind::LeftBrace,
        TokenKind::RightBrace,
        false,
        is_name,
        |p| {
            let field_start = p.tok().span.start;
            let name = p.name();
            let t = p.tok();
            if t.kind == TokenKind::Colon {
                p.next();
                colons.push(p.fix(t.span, " ="));
            } else {
                p.expect(TokenKind::Assign);
            }
            let value = p.expr(operators::Precedence::Lowest);
            p.add(field_start, ast::FieldValue { name, value })
        },
    );
    if !colons.is_empty() {
        let d = Diagnostic::new(
            diagnostics::EXPECTED_TOKEN,
            "expected `=` after struct literal field name",
        )
        .label(&p.tree.file, colons[0].span, "");
        p.repair(d.help("set a field with `=`", colons));
    }
    p.add(start, ast::Expr::StructLiteral { ty, fields })
}

#[cfg(test)]
mod tests {
    //! Reading structs.

    use crate::testing;
    use arena::Id;
    use diagnostics::Diagnostic;
    use source::Span;
    use tree::ast;

    /// `text` with the fixes of the first help of `d` applied.
    fn fixed(text: &str, d: &Diagnostic) -> String {
        let mut text = text.to_string();
        for fix in d.notes[0].fixes().iter().rev() {
            text.replace_range(fix.span.range(), &fix.new_text);
        }
        text
    }

    /// Checks that `text` gives exactly one error, `message`, and that its fix
    /// (when `want_fixed` is given) gives `want_fixed`.
    fn one_error(text: &str, message: &str, want_fixed: Option<&str>) {
        let (_, errors) = testing::parse_text(text);
        let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
        assert_eq!(got, [message], "{text:?}");
        if let Some(want) = want_fixed {
            assert_eq!(fixed(text, &errors[0]), want, "{text:?}");
        }
    }

    fn written(tree: &ast::Tree, span: Span) -> &str {
        tree.file.slice(span)
    }

    /// The fields of the struct type `ty`: each as `name: type`, the type as
    /// written, and the type.
    fn fields(tree: &ast::Tree, ty: Id<ast::Type>) -> Vec<(String, Id<ast::Type>)> {
        let ast::Type::Struct { fields } = tree.types[ty] else {
            panic!("not a struct type: {:?}", tree.types[ty])
        };
        let field = |id| {
            let ast::Field { name, ty } = tree.children.fields[id];
            let field = format!(
                "{}: {}",
                name.text(&tree.file),
                written(tree, tree.types.span(ty))
            );
            (field, ty)
        };
        tree.children.lists.items(fields).map(field).collect()
    }

    /// The struct literal `expr`: its type as written, if any, and each field
    /// as `name = value`, the value as written.
    fn literal(tree: &ast::Tree, expr: Id<ast::Expr>) -> (Option<&str>, Vec<String>) {
        let ast::Expr::StructLiteral { ty, fields } = tree.exprs[expr] else {
            panic!("not a struct literal: {:?}", tree.exprs[expr])
        };
        let field = |id| {
            let ast::FieldValue { name, value } = tree.children.field_values[id];
            format!(
                "{} = {}",
                name.text(&tree.file),
                written(tree, tree.exprs.span(value))
            )
        };
        let ty = ty.map(|ty| written(tree, tree.types.span(ty)));
        (ty, tree.children.lists.items(fields).map(field).collect())
    }

    /// A struct declaration's type is a struct type, which a field's type can
    /// be too; a `;` after its `}` is allowed.
    #[test]
    fn parses_declarations() {
        let text = "struct Pair<T> {\n    first: T,\n    second: struct { flag: bool },\n};\n";
        let (tree, errors) = testing::parse_text(text);
        assert!(errors.is_empty(), "{errors:?}");
        let declarations: Vec<_> = tree.declarations().collect();
        assert_eq!(declarations.len(), 1);
        let ast::Decl::Type(declared) = tree.decls[declarations[0]] else {
            panic!("not a type declaration")
        };
        assert_eq!(declared.name.text(&tree.file), "Pair");
        assert_eq!(declared.type_parameters.len(), 1);
        let found = fields(&tree, declared.ty);
        let names: Vec<_> = found.iter().map(|(field, _)| field.as_str()).collect();
        assert_eq!(names, ["first: T", "second: struct { flag: bool }"]);
        let nested = fields(&tree, found[1].1);
        assert_eq!(nested[0].0, "flag: bool");
        assert_eq!(nested.len(), 1);
    }

    #[test]
    fn declaration_errors() {
        one_error(
            "struct S { a i32 }",
            "expected `:`, found `identifier`",
            None,
        );
    }

    /// A literal with a type, without one, and as an argument.
    #[test]
    fn parses_expressions() {
        let text = "fn f() {\n    Point.{ x = 1, y = -2 };\n    .{ a = &mut v, b = !flag };\n    line(.{ end = Box::<i32>::new(1) });\n}\n";
        let (tree, errors) = testing::parse_text(text);
        assert!(errors.is_empty(), "{errors:?}");
        let found = testing::statements(&tree);
        assert_eq!(
            literal(&tree, found[0]),
            (Some("Point"), vec!["x = 1".into(), "y = -2".into()])
        );
        assert_eq!(
            literal(&tree, found[1]),
            (None, vec!["a = &mut v".into(), "b = !flag".into()])
        );
        let ast::Expr::Call { arguments, .. } = tree.exprs[found[2]] else {
            panic!("not a call")
        };
        let argument = tree.children.lists.items(arguments).next().unwrap();
        assert_eq!(
            literal(&tree, argument),
            (None, vec!["end = Box::<i32>::new(1)".into()])
        );
    }

    #[test]
    fn expression_errors() {
        one_error(
            "fn f() { .{ a: 1, b: 2 }; }",
            "expected `=` after struct literal field name",
            None,
        );
    }

    /// In an expression, type arguments follow `::`, before `.{` too.
    #[test]
    fn comparison_is_not_type_arguments() {
        let (tree, errors) = testing::parse_text("fn f() { Pair::<A, B>.{}; }");
        assert!(errors.is_empty(), "{errors:?}");
        assert_eq!(
            literal(&tree, testing::statements(&tree)[0]),
            (Some("Pair::<A, B>"), Vec::new())
        );
    }

    /// Every span of a tree read from broken structs lies in the text and
    /// starts before it ends.
    #[test]
    fn spans_lie_in_the_text() {
        for text in [
            "fn f() { .{ a = }; }",
            "fn f() { .{ a: 1, b: 2 }; }",
            "fn f() { a::<b>.{}; }",
            "struct",
            "struct S {",
            "struct S { a: }",
            "struct S {} struct",
            "fn f() {\nstruct S {}",
        ] {
            let (tree, _) = testing::parse_text(text);
            let spans = (tree.exprs.ids().map(|id| tree.exprs.span(id)))
                .chain(tree.types.ids().map(|id| tree.types.span(id)))
                .chain(tree.decls.ids().map(|id| tree.decls.span(id)))
                .chain(tree.stmts.ids().map(|id| tree.stmts.span(id)))
                .chain(tree.blocks.ids().map(|id| tree.blocks.span(id)))
                .chain(
                    tree.children
                        .fields
                        .ids()
                        .map(|id| tree.children.fields.span(id)),
                )
                .chain(
                    (tree.children.field_values.ids())
                        .map(|id| tree.children.field_values.span(id)),
                );
            for span in spans {
                let (start, end) = (span.start as usize, span.end as usize);
                assert!(start <= end && end <= text.len(), "{text:?}: {span:?}");
            }
        }
    }

    /// A closer that comes later means what stands before it does not belong:
    /// that is reported once, and the list ends at its own closer.
    #[test]
    fn missing_token_place() {
        let (tree, errors) =
            testing::parse_text("fn f() { let p = Point.{ .x = 1, y = 2 }; }\nfn g() {}");
        let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
        assert_eq!(got, ["expected `}`, found `.`"]);
        assert_eq!(tree.top_level.len(), 2);
    }

    /// Extra and trailing commas are left to a lint; a `;` for a `,` is
    /// repaired with one error per list.
    #[test]
    fn separators() {
        for text in [
            "struct S { a: i32, b: i32, }",
            "fn f() { let p = .{ a = 1,, }; }",
        ] {
            assert!(testing::messages(text).is_empty(), "{text:?}");
        }
        for (text, want) in [
            ("struct S { a: i32; b: i32 }", "struct S { a: i32, b: i32 }"),
            (
                "struct S { a: i32; b: i32; }",
                "struct S { a: i32, b: i32 }",
            ),
            (
                "fn f() { let p = .{ a = 1; b = 2 }; }",
                "fn f() { let p = .{ a = 1, b = 2 }; }",
            ),
        ] {
            one_error(text, "expected `,` between items, found `;`", Some(want));
        }
    }
}
