use model::ast::Decl;
use model::ast::Stmt;
use model::ast::Tree;

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

/// An expression with each binary operation in parentheses.
fn grouped(t: &Tree, id: Id<Expr>) -> String {
    let text = |span| t.file.slice(span).to_string();
    match t.exprs[id] {
        Expr::Binary { op, left, right } => {
            format!(
                "({} {} {})",
                grouped(t, left),
                op.symbol(),
                grouped(t, right)
            )
        }
        Expr::Cast { value, ty } => {
            format!("({} as {})", grouped(t, value), text(t.types.span(ty)))
        }
        Expr::Is { value, case } => {
            format!("({} is {})", grouped(t, value), text(t.types.span(case)))
        }
        Expr::OrElse { value, fallback } => {
            format!("({} orelse {})", grouped(t, value), grouped(t, fallback))
        }
        Expr::Pipe { value, call } => {
            format!("({} |> {})", grouped(t, value), grouped(t, call))
        }
        Expr::Range {
            inclusive,
            start,
            end,
            step,
        } => {
            let part = |part: Option<Id<Expr>>| part.map_or(String::new(), |e| grouped(t, e));
            let op = if inclusive { "..=" } else { ".." };
            let step = step.map_or(String::new(), |e| format!(":{}", grouped(t, e)));
            format!("({}{op}{}{step})", part(start), part(end))
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
        Expr::Call { callee, arguments } => {
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
/// one precedence group to the left.
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
        ("a + b as i64", "(a + (b as i64))"),
        ("a as i32 * b", "((a as i32) * b)"),
        ("a is E::B && c", "((a is E::B) && c)"),
        ("a orelse b orelse c", "(a orelse (b orelse c))"),
        ("a || b orelse c", "((a || b) orelse c)"),
        ("a |> f(b) |> g()", "((a |> f(b)) |> g())"),
        ("a |> len() as i32", "((a |> len()) as i32)"),
        ("a + b |> f()", "((a + b) |> f())"),
        ("x.y.z[0][i..j]", "x.y.z[0][i..j]"),
        ("a + b..c * d", "((a + b)..(c * d))"),
        ("a orelse b..c", "(a orelse (b..c))"),
        ("a..b orelse c", "(a..(b orelse c))"),
        ("a..=b:c + d", "(a..=b:(c + d))"),
        ("..b as i64", "(..(b as i64))"),
        ("a..b..c", "((a..b)..c)"),
        ("-r.* * r.x.*", "(-r.* * r.x.*)"),
        ("&mut r.*.x", "&mut r.*.x"),
    ] {
        let (tree, errors) = parse_text(&format!("fn f() {{ {text}; }}"));
        assert!(errors.is_empty(), "{text}: {errors:?}");
        assert_eq!(grouped(&tree, first_expression(&tree)), want, "{text}");
    }
}

#[test]
fn parses_expressions() {
    let text = r#"fn f() {
    c"c";
    'x';
    b'y';
    true;
    none;
    Point.{ x = 1, y = -2 };
    .{ a = &mut v, b = @raw };
    Shape::Line with .{ end = Box<i32>::new(1) };
    [_]u8{ 1, 2, 4..=8:2..., (0..3)..., a... };
    free(p);
    println(items[1..=n]);
}
"#;
    let want = r#"module
  function declaration
    declared name f
    block
      expression statement
        string literal c string c"c"
      expression statement
        character literal 'x'
      expression statement
        character literal byte character b'y'
      expression statement
        bool literal true
      expression statement
        none literal none
      expression statement
        struct literal
          type name Point
          field value
            field name x
            number literal 1
          field value
            field name y
            unary expression -
              number literal 2
      expression statement
        struct literal
          field value
            field name a
            reference expression mut
              name reference v
          field value
            field name b
            raw address expression
              name reference raw
      expression statement
        variant literal
          path
            type name Shape
            type name Line
          struct literal
            field value
              field name end
              call expression
                path
                  generic type
                    type name Box
                    type name i32
                  type name new
                number literal 1
      expression statement
        array literal
          fixed array type
            inferred length _
            type name u8
          number literal 1
          number literal 2
          spread
            range ..=
              number literal 4
              number literal 8
              number literal 2
          spread
            parentheses
              range ..
                number literal 0
                number literal 3
          spread
            name reference a
      expression statement
        free expression
          name reference p
      expression statement
        print expression println
          index expression
            name reference items
            range ..=
              number literal 1
              name reference n
"#;
    assert!(messages(text).is_empty());
    assert_eq!(dump_of(text), want);
}

#[test]
fn expression_errors() {
    for (text, message) in [
        ("a[1..=];", "inclusive range requires an end bound"),
        ("a is b;", "enum case must use a fully named path"),
        ("a |> b;", "pipe target must be a free-function call"),
        ("a |> b.c();", "pipe cannot call a method"),
        (
            ".{ a: 1, b: 2 };",
            "expected `=` after struct literal field name",
        ),
        ("[x]i32{};", "expected array length"),
        (
            "[]i32{};",
            "incomplete type `[]`: add a size or a reference",
        ),
        ("a.;", "expected `identifier`, found `;`"),
        ("f(a...);", "expected `)`, found `...`"),
    ] {
        one_error(&format!("fn f() {{ {text} }}"), message, None);
    }
}

/// `<` begins type arguments only when `::` or `.{` follows the matching `>`.
#[test]
fn comparison_is_not_type_arguments() {
    for (text, want) in [
        ("a < b && c > d;", "binary"),
        ("Box<T>::new();", "call"),
        ("Pair<A, B>.{};", "struct literal"),
        ("a < b;", "binary"),
        ("f(a < b, c > d);", "call"),
    ] {
        let (tree, errors) = parse_text(&format!("fn f() {{ {text} }}"));
        assert!(errors.is_empty(), "{text}: {errors:?}");
        let got = match tree.exprs[first_expression(&tree)] {
            Expr::Binary { .. } => "binary",
            Expr::Call { .. } => "call",
            Expr::StructLiteral { .. } => "struct literal",
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
