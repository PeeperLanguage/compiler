use syntax::ast::Decl;
use syntax::ast::Stmt;
use syntax::ast::StringKind;
use syntax::ast::Tree;

use super::*;
use crate::tests::dump_of;
use crate::tests::messages;
use crate::tests::one_error;
use crate::tests::parse_text;

/// There is no unary `+`: a leading one is reported, and its fix removes it.
#[test]
fn leading_plus() {
    one_error(
        "fn f() { let a = +5; }",
        "`+` is not a prefix operator",
        Some("fn f() { let a = 5; }"),
    );
}

/// An operator is written as the token it is read from: each binary and
/// unary operator comes from exactly one operator token, whose text is
/// the operator's symbol.
#[test]
fn operators_are_written_as_their_tokens() {
    let (mut binary, mut unary) = (0, 0);
    for &kind in token::OPERATORS {
        for text in [
            format!("fn f() {{ a {} b; }}", kind.text()),
            format!("fn f() {{ {}a; }}", kind.text()),
        ] {
            let (tree, errors) = parse_text(&text);
            if !errors.is_empty() {
                continue;
            }
            for id in tree.exprs.ids() {
                match tree.exprs[id] {
                    Expr::Binary { op, .. } => {
                        assert_eq!(op.symbol(), kind.text(), "{text}");
                        binary += 1;
                    }
                    Expr::Unary { op, .. } => {
                        assert_eq!(op.symbol(), kind.text(), "{text}");
                        unary += 1;
                    }
                    _ => {}
                }
            }
        }
    }
    assert_eq!((binary, unary), (18, 3));
}

/// The expression of the first statement of the first function.
fn first_expression(tree: &Tree) -> Id<Expr> {
    let decl = tree.declarations().next().unwrap();
    let Decl::Function(f) = tree.decls[decl] else {
        panic!("not a function")
    };
    let statement = tree
        .children
        .lists
        .items(tree.blocks[f.body.unwrap()].statements)
        .next()
        .unwrap();
    let Stmt::Expression(e) = tree.stmts[statement] else {
        panic!("not an expression statement")
    };
    e
}

/// An expression with each binary operation and cast in parentheses.
fn grouped(t: &Tree, id: Id<Expr>) -> String {
    let text = |span| t.file.slice(span).to_string();
    match t.exprs[id] {
        Expr::Cast { value, ty } => {
            format!("({} as {})", grouped(t, value), text(t.types.span(ty)))
        }
        Expr::Binary { op, left, right } => {
            format!(
                "({} {} {})",
                grouped(t, left),
                op.symbol(),
                grouped(t, right)
            )
        }
        Expr::Unary { value, .. } | Expr::Reference { value, .. } => {
            let (span, inner) = (t.exprs.span(id), t.exprs.span(value));
            let prefix = t.file.text[span.start as usize..inner.start as usize].trim();
            let space = if let Expr::Reference { mutable: true, .. } = t.exprs[id] {
                " "
            } else {
                ""
            };
            format!("{prefix}{space}{}", grouped(t, value))
        }
        Expr::Selector { value, field } => {
            format!("{}.{}", grouped(t, value), text(field.span))
        }
        Expr::Dereference(value) => format!("{}.*", grouped(t, value)),
        Expr::Index { value, index } => {
            format!("{}[{}]", grouped(t, value), text(t.exprs.span(index)))
        }
        Expr::Call {
            callee, arguments, ..
        } => {
            let arguments: Vec<_> = t
                .children
                .lists
                .items(arguments)
                .map(|a| grouped(t, a))
                .collect();
            format!("{}({})", grouped(t, callee), arguments.join(", "))
        }
        _ => text(t.exprs.span(id)),
    }
}

/// How operators group, from loosest to tightest, and that operators of
/// one precedence group to the left. A cast binds tighter than every binary
/// operator.
#[test]
fn precedence() {
    for (text, want) in [
        ("a || b && c", "(a || (b && c))"),
        ("a | b ^ c & d", "(a | (b ^ (c & d)))"),
        ("a & b == c", "(a & (b == c))"),
        ("a == b < c", "(a == (b < c))"),
        ("a < b << c", "(a < (b << c))"),
        ("a << b + c", "(a << (b + c))"),
        ("a + b * c", "(a + (b * c))"),
        ("a - b - c", "((a - b) - c)"),
        ("a * b % c / d", "(((a * b) % c) / d)"),
        ("(a + b) * f(c, d + e)", "((a + b) * f(c, (d + e)))"),
        ("a != b >= c > d <= e", "(a != (((b >= c) > d) <= e))"),
        ("-a * b", "(-a * b)"),
        ("-a.b(c)[d]", "-a.b(c)[d]"),
        ("!a && b", "(!a && b)"),
        ("&mut a + b", "(&mut a + b)"),
        ("x.y.z[0][i]", "x.y.z[0][i]"),
        ("-r.* * r.x.*", "(-r.* * r.x.*)"),
        ("&mut r.*.x", "&mut r.*.x"),
        ("a + b as i64", "(a + (b as i64))"),
        ("a as i32 * b", "((a as i32) * b)"),
    ] {
        let (tree, errors) = parse_text(&format!("fn f() {{ {text}; }}"));
        assert!(errors.is_empty(), "{text}: {errors:?}");
        assert_eq!(grouped(&tree, first_expression(&tree)), want, "{text}");
    }
}

#[test]
fn parses_expressions() {
    let text = r#"fn f() {
    none;
    Box::<i32>::new(1);
    Max::<f64, i32>(1);
    list::Sum::<u8>();
    sum(1, (a)..., &mut b...);
    items[n];
}
"#;
    let want = r#"module
  function declaration
    declared name f
    block
      expression statement
        none literal none
      expression statement
        call expression
          path
            generic type
              type name Box
              type name i32
            type name new
          number literal 1
      expression statement
        call expression
          name reference Max
          type name f64
          type name i32
          number literal 1
      expression statement
        call expression
          path
            type name list
            type name Sum
          type name u8
      expression statement
        call expression
          name reference sum
          number literal 1
          spread
            parentheses
              name reference a
          spread
            reference expression mut
              name reference b
      expression statement
        index expression
          name reference items
          name reference n
"#;
    assert!(messages(text).is_empty());
    assert_eq!(dump_of(text), want);
}

#[test]
fn expression_errors() {
    one_error("fn f() { a.; }", "expected `identifier`, found `;`", None);
}

/// Values after a slice type are an array's, lent: `&[_]i32{ 1, 2 }`.
#[test]
fn slice_type_with_values() {
    one_error(
        "fn f() { let s = &[]i32{ 1, 2 }; }",
        "`&[]i32` is a slice type, so it takes no values",
        Some("fn f() { let s = &[_]i32{ 1, 2 }; }"),
    );
}

/// A spelling that can only be a type is read as one where a value could go,
/// for an argument that takes a type.
#[test]
fn type_expressions() {
    let text = "fn f() { g(&[]i32, &mut []u8, &x); }\n";
    let want = r#"module
  function declaration
    declared name f
    block
      expression statement
        call expression
          name reference g
          type expression
            slice type
              type name i32
          type expression
            slice type mut
              type name u8
          reference expression
            name reference x
"#;
    assert!(messages(text).is_empty());
    assert_eq!(dump_of(text), want);
}

/// In an expression `<` compares; type arguments follow `::`.
#[test]
fn comparison_is_not_type_arguments() {
    for (text, want) in [
        ("a < b && c > d;", "binary"),
        ("Box::<T>::new();", "call"),
        ("a < b;", "binary"),
        ("f(a < b, c > d);", "call"),
        ("Max::<f64>(1);", "call"),
        ("a < b > (c);", "binary"),
        ("a < b && c > (d);", "binary"),
    ] {
        let (tree, errors) = parse_text(&format!("fn f() {{ {text} }}"));
        assert!(errors.is_empty(), "{text}: {errors:?}");
        let got = match tree.exprs[first_expression(&tree)] {
            Expr::Binary { .. } => "binary",
            Expr::Call { .. } => "call",
            other => panic!("{text}: {other:?}"),
        };
        assert_eq!(got, want, "{text}");
    }
}

/// Number literal suffixes, with the old compiler's messages. Digits outside
/// the base are the lexer's to report; a leading zero (007) is left to a
/// later lint pass.
#[test]
fn number_checks() {
    for (literal, message) in [
        ("1i", "invalid numeric literal suffix i"),
        ("1i_32", "invalid numeric literal suffix i_32"),
        ("1i8_", "invalid numeric literal suffix i8_"),
        ("1u", "invalid numeric literal suffix u"),
        ("1f", "invalid numeric literal suffix f"),
        ("0b1i", "invalid numeric literal suffix i"),
        ("0o7u", "invalid numeric literal suffix u"),
        (
            "2.4i32",
            "integer suffix i32 cannot be used on a float literal",
        ),
        (
            "1e3u8",
            "integer suffix u8 cannot be used on a float literal",
        ),
        ("0", ""),
        ("0.5", ""),
        ("007", ""),
        ("01i32", ""),
        ("01f32", ""),
        ("1.5f32", ""),
        ("0x1f_u8", ""),
        ("0xffu24", ""),
        ("1_000i64", ""),
        ("1e3f64", ""),
    ] {
        let text = format!("fn f() {{ {literal}; }}");
        let (_, errors) = parse_text(&text);
        if message.is_empty() {
            assert!(errors.is_empty(), "{literal}: {errors:?}");
            continue;
        }
        let start = text.find(literal).unwrap();
        assert!(
            errors.len() == 1
                && errors[0].message == message
                && errors[0].labels[0].span.range() == (start..start + literal.len()),
            "{literal}: {errors:?}"
        );
    }
}

/// `true` and `false` are bool literals, each spanning its word, and an
/// operand like any value.
#[test]
fn bool_literals() {
    let (tree, errors) = parse_text("fn f() { true; false; !true == false; }");
    assert!(errors.is_empty(), "{errors:?}");
    let literals: Vec<(bool, &str)> = (tree.exprs.ids())
        .filter_map(|id| match tree.exprs[id] {
            Expr::Bool(value) => Some((value, tree.file.slice(tree.exprs.span(id)))),
            _ => None,
        })
        .collect();
    assert_eq!(
        literals,
        [
            (true, "true"),
            (false, "false"),
            (true, "true"),
            (false, "false")
        ]
    );
}

/// `"..."` is a plain string literal and `c"..."` a C one, each spanning
/// its quotes.
#[test]
fn string_literals() {
    let (tree, errors) = parse_text(r#"fn f() { g("a", c"b"); }"#);
    assert!(errors.is_empty(), "{errors:?}");
    let literals: Vec<(StringKind, &str)> = (tree.exprs.ids())
        .filter_map(|id| match tree.exprs[id] {
            Expr::String(kind) => Some((kind, tree.file.slice(tree.exprs.span(id)))),
            _ => None,
        })
        .collect();
    assert_eq!(
        literals,
        [(StringKind::Plain, "\"a\""), (StringKind::C, "c\"b\"")]
    );
}
