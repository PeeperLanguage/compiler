//! Reading structs. The parser's tree dump is its own tests', so these look
//! at the tree.

use std::sync::Arc;

use arena::Id;
use arena::List;
use diagnostics::Diagnostic;
use parser::Grammar;
use parser::Rules;
use source::SourceFile;
use source::Span;
use syntax::ast;
use syntax::ast::Language;
use syntax::operators::Precedence;

use super::*;

/// The language the struct tests are written in: the core and structs.
#[derive(Clone, Copy, Debug)]
enum Structs {}

impl Language for Structs {
    type Expr = StructLiteral<Structs>;
    type Type = StructType<Structs>;

    fn precedence(_: &StructLiteral<Structs>) -> Precedence {
        Precedence::Call
    }

    fn fields(ty: &StructType<Structs>) -> Option<List<ast::Field<Structs>>> {
        Some(ty.fields)
    }

    fn methods(_: &StructType<Structs>) -> Option<List<ast::Decl<Structs>>> {
        None
    }

    fn pointee(_: &StructType<Structs>) -> Option<Id<ast::Type<Structs>>> {
        None
    }

    fn string_kind(_: &StructLiteral<Structs>) -> Option<ast::StringKind> {
        None
    }
}

static RULES: Rules<Structs> = {
    let mut rules = Rules::core();
    parse::rows(&mut rules);
    rules
};

impl Grammar for Structs {
    fn rules() -> &'static Rules<Structs> {
        &RULES
    }
}

type Tree = ast::Tree<Structs>;

fn parse_text(text: &str) -> (Tree, Vec<Diagnostic>) {
    parser::parse(&Arc::new(SourceFile::new("t.peep", text)))
}

fn messages(text: &str) -> Vec<String> {
    parse_text(text).1.into_iter().map(|e| e.message).collect()
}

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
    let (_, errors) = parse_text(text);
    let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
    assert_eq!(got, [message], "{text:?}");
    if let Some(want) = want_fixed {
        assert_eq!(fixed(text, &errors[0]), want, "{text:?}");
    }
}

fn written(tree: &Tree, span: Span) -> &str {
    tree.file.slice(span)
}

/// The fields of the struct type `ty`: each as `name: type`, the type as
/// written, and the type.
fn fields(tree: &Tree, ty: Id<ast::Type<Structs>>) -> Vec<(String, Id<ast::Type<Structs>>)> {
    let ast::Type::Feature(StructType { fields }) = tree.types[ty] else {
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

/// The expressions of the statements of the first function of `tree`.
fn statements(tree: &Tree) -> Vec<Id<ast::Expr<Structs>>> {
    let decl = tree.declarations().next().unwrap();
    let ast::Decl::Function(function) = tree.decls[decl] else {
        panic!("not a function")
    };
    let statements = tree.blocks[function.body.unwrap()].statements;
    let expression = |statement| match tree.stmts[statement] {
        ast::Stmt::Expression(expr) => expr,
        other => panic!("not an expression statement: {other:?}"),
    };
    tree.children
        .lists
        .items(statements)
        .map(expression)
        .collect()
}

/// The struct literal `expr`: its type as written, if any, and each field
/// as `name = value`, the value as written.
fn literal(tree: &Tree, expr: Id<ast::Expr<Structs>>) -> (Option<&str>, Vec<String>) {
    let ast::Expr::Feature(StructLiteral { ty, fields }) = tree.exprs[expr] else {
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
    let (tree, errors) = parse_text(text);
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

/// A literal with a type, without one, and as a variant's payload.
#[test]
fn parses_expressions() {
    let text = "fn f() {\n    Point.{ x = 1, y = -2 };\n    .{ a = &mut v, b = !flag };\n    Shape::Line with .{ end = Box<i32>::new(1) };\n}\n";
    let (tree, errors) = parse_text(text);
    assert!(errors.is_empty(), "{errors:?}");
    let found = statements(&tree);
    assert_eq!(
        literal(&tree, found[0]),
        (Some("Point"), vec!["x = 1".into(), "y = -2".into()])
    );
    assert_eq!(
        literal(&tree, found[1]),
        (None, vec!["a = &mut v".into(), "b = !flag".into()])
    );
    let ast::Expr::VariantLiteral { case, payload } = tree.exprs[found[2]] else {
        panic!("not a variant literal")
    };
    assert_eq!(written(&tree, tree.types.span(case)), "Shape::Line");
    assert_eq!(
        literal(&tree, payload),
        (None, vec!["end = Box<i32>::new(1)".into()])
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

/// `<` begins type arguments when `.{` follows the matching `>`.
#[test]
fn comparison_is_not_type_arguments() {
    let (tree, errors) = parse_text("fn f() { Pair<A, B>.{}; }");
    assert!(errors.is_empty(), "{errors:?}");
    assert_eq!(
        literal(&tree, statements(&tree)[0]),
        (Some("Pair<A, B>"), Vec::new())
    );
}

/// Every span of a tree read from broken structs lies in the text and
/// starts before it ends.
#[test]
fn spans_lie_in_the_text() {
    for text in [
        "fn f() { .{ a = }; }",
        "fn f() { .{ a: 1, b: 2 }; }",
        "fn f() { a<b>.{}; }",
        "struct",
        "struct S {",
        "struct S { a: }",
        "struct S {} struct",
        "fn f() {\nstruct S {}",
    ] {
        let (tree, _) = parse_text(text);
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
                (tree.children.field_values.ids()).map(|id| tree.children.field_values.span(id)),
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
    let (tree, errors) = parse_text("fn f() { let p = Point.{ .x = 1, y = 2 }; }\nfn g() {}");
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
        assert!(messages(text).is_empty(), "{text:?}");
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
