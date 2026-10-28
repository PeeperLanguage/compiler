use crate::tests::dump_of;
use crate::tests::messages;
use crate::tests::one_error;

/// A `return` without a value needs only its `;`: one missing at a line
/// end is read past with a fix, one missing inside a line is reported.
#[test]
fn lone_return() {
    one_error(
        "fn f() {\n    return\n}",
        "expected `;`, found `}`",
        Some("fn f() {\n    return;\n}"),
    );
    one_error("fn f() { return }", "expected `;`, found `}`", None);
}

#[test]
fn parses_statements() {
    let text = "fn f() {
    let mut n: i32 = 0;
    let .{ x, y as z, _ } = point;
    const limit = 10;
    n = n + 1;
    if n == 1 { } else if n == 2 { } else { }
    for { break; }
    for n < limit { continue; }
    for item in items { }
    for i, item in items[1..] { }
    for 0..n { }
    for v in n..=0:2 { }
    match shape {
        Shape::Dot => { }
        Shape::Line with .{ end } => { }
        Shape::Maybe with none => { }
        Shape::Wrap with Inner::Leaf with value => { }
        _ => { }
    }
    panic \"stop\";
    { }
}
";
    let want = "module
  function declaration
    declared name f
    block
      let statement mut
        declared name n
        type name i32
        number literal 0
      destructure statement
        struct pattern
          field pattern
            field name x
          field pattern
            field name y
            declared name z
          rest pattern _
        name reference point
      const declaration
        declared name limit
        number literal 10
      assign statement
        name reference n
        binary expression +
          name reference n
          number literal 1
      if statement
        binary expression ==
          name reference n
          number literal 1
        block { }
        if statement
          binary expression ==
            name reference n
            number literal 2
          block { }
          block { }
      loop statement
        block
          break statement break;
      loop statement
        binary expression <
          name reference n
          name reference limit
        block
          continue statement continue;
      for in statement
        declared name item
        name reference items
        block { }
      for in statement
        declared name i
        declared name item
        index expression
          name reference items
          range ..
            number literal 1
        block { }
      loop statement
        range ..
          number literal 0
          name reference n
        block { }
      for in statement
        declared name v
        range ..=
          name reference n
          number literal 0
          number literal 2
        block { }
      match statement
        name reference shape
        match arm
          case pattern
            path
              type name Shape
              type name Dot
          block { }
        match arm
          case pattern
            path
              type name Shape
              type name Line
            struct pattern
              field pattern
                field name end
          block { }
        match arm
          case pattern
            path
              type name Shape
              type name Maybe
            none pattern none
          block { }
        match arm
          case pattern
            path
              type name Shape
              type name Wrap
            case pattern
              path
                type name Inner
                type name Leaf
              declared name value
          block { }
        match arm
          wildcard pattern _
          block { }
      panic statement
        string literal \"stop\"
      block { }
";
    assert!(messages(text).is_empty());
    assert_eq!(dump_of(text), want);
}

/// `if a = b` is read as the comparison it was meant to be.
#[test]
fn assignment_in_condition_is_read_as_equal_to() {
    let (tree, _) = crate::tests::parse_text("fn f() { if a = b {} }");
    let condition = tree
        .stmts
        .ids()
        .find_map(|statement| match tree.stmts[statement] {
            model::ast::Stmt::If { condition, .. } => Some(condition),
            _ => None,
        })
        .expect("an if statement");
    assert!(matches!(
        tree.exprs[condition],
        model::ast::Expr::Binary {
            op: model::operators::BinaryOp::EqualTo,
            ..
        }
    ));
}

#[test]
fn statement_errors() {
    for (text, message, fixed) in [
        (
            "if a = b {}",
            "expected `==` in condition, found `=`",
            Some("if a == b {}"),
        ),
        ("if a", "missing if body", None),
        ("if a {} else", "missing else body", None),
        ("for x in items", "missing for body", None),
        ("for a.b in x {}", "invalid loop variable", None),
        ("break outer;", "labeled break is not supported yet", None),
        (
            "match x { A::B {} }",
            "expected `=>` after match case",
            Some("match x { A::B => {} }"),
        ),
        (
            "match x { A::B => {}, _ => {} }",
            "match arms do not use commas",
            Some("match x { A::B => {} _ => {} }"),
        ),
        (
            "match x { a => {} }",
            "enum case must use a fully named path",
            None,
        ),
        (
            "match x { A::B with => {} }",
            "expected a pattern after `with`",
            None,
        ),
        (
            "let .{ a = b } = v;",
            "a destructure field is renamed with `as`",
            Some("let .{ a as b } = v;"),
        ),
        (
            "let .{ _, a } = v;",
            "`_` must be the last destructure item",
            None,
        ),
    ] {
        let fixed = fixed.map(|f| format!("fn f() {{ {f} }}"));
        one_error(&format!("fn f() {{ {text} }}"), message, fixed.as_deref());
    }
}
