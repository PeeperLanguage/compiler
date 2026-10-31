use crate::tests::dump_of;
use crate::tests::messages;
use crate::tests::one_error;

#[test]
fn parses_variadic_parameters() {
    let text = "fn f(first: i32, rest: ...mut i32);\nfn g(_: ...i32);\n";
    let want = "module
  function declaration
    declared name f
    parameter
      declared name first
      type name i32
    variadic parameter
      declared name rest
      slice type mut
        type name i32
  function declaration
    declared name g
    variadic parameter
      slice type
        type name i32
";
    assert!(messages(text).is_empty());
    assert_eq!(dump_of(text), want);
}

#[test]
fn parses_signatures() {
    let text = "fn (self: &mut Counter) Add<T, U>(mut delta: i32, scale: f64 = 1, &Self) -> &T in (self, delta) {}
fn exit(code: i32);
fn Use(callback: fn(value: &Box, i32) -> &Box in value, view: [&]u8, edit: [&mut]u8, grid: [4]rawptr, cell: **u8, untyped: *rawptr) -> ?a::Box<Box<T>>;
";
    let want = "module
  function declaration
    parameter
      declared name self
      reference type mut
        type name Counter
    declared name Add
    declared name T
    declared name U
    parameter mut
      declared name delta
      type name i32
    parameter
      declared name scale
      type name f64
      number literal 1
    parameter
      reference type
        type name Self
    reference type
      type name T
    name reference self
    name reference delta
    block {}
  function declaration
    declared name exit
    parameter
      declared name code
      type name i32
  function declaration
    declared name Use
    parameter
      declared name callback
      function type
        parameter
          declared name value
          reference type
            type name Box
        parameter
          type name i32
        reference type
          type name Box
        name reference value
    parameter
      declared name view
      slice type
        type name u8
    parameter
      declared name edit
      slice type mut
        type name u8
    parameter
      declared name grid
      fixed array type
        number literal 4
        raw pointer type rawptr
    parameter
      declared name cell
      pointer type
        pointer type
          type name u8
    parameter
      declared name untyped
      pointer type
        raw pointer type rawptr
    optional type
      path
        type name a
        generic type
          type name Box
          generic type
            type name Box
            type name T
";
    assert!(messages(text).is_empty());
    assert_eq!(dump_of(text), want);
}

#[test]
fn signature_errors() {
    for (text, message, fixed) in [
        (
            "fn f() i32 {}",
            "expected `->` before the return type",
            Some("fn f() -> i32 {}"),
        ),
        (
            "fn f(mut mut a: i32) {}",
            "`mut` is repeated",
            Some("fn f(mut a: i32) {}"),
        ),
        (
            "fn f(mut i32) {}",
            "mutable parameter requires a named binding",
            None,
        ),
        ("fn f(a: [n]i32) {}", "expected array length", None),
        (
            "fn f(a: []i32) {}",
            "incomplete type `[]`: add a size or a reference",
            None,
        ),
        ("fn f(a: ) {}", "expected type, found `)`", None),
        ("fn f() -> i32", "missing function body", None),
        ("fn f<T U>() {}", "expected `,`", Some("fn f<T, U>() {}")),
    ] {
        one_error(text, message, fixed);
    }
}

#[test]
fn parses_declarations() {
    let text = r#"/// The answer.
/// Twice checked.
const Answer: i32 = 42;
const Untyped = 1;
#[extern("rt_exit")]
fn exit(code: i32);
struct Pair<T> {
    first: T,
    second: struct { flag: bool },
};
enum Status { Ready, Waiting: Pair<i32>, }
iface Summer {
    fn (&Self) sum() -> i32,
    fn (self: &Self) add<T>(value: T)
}
type Alias<T> = ?T;
"#;
    let want = r#"module
  const declaration
    metadata
      doc comment /// The answer.
/// Twice checked.
    declared name Answer
    type name i32
    number literal 42
  const declaration
    declared name Untyped
    number literal 1
  function declaration
    metadata
      attribute
        name reference extern
        string literal "rt_exit"
    declared name exit
    parameter
      declared name code
      type name i32
  struct declaration
    declared name Pair
    declared name T
    struct type
      field
        declared name first
        type name T
      field
        declared name second
        struct type
          field
            declared name flag
            type name bool
  enum declaration
    declared name Status
    enum type
      variant
        declared name Ready
      variant
        declared name Waiting
        generic type
          type name Pair
          type name i32
  interface declaration
    declared name Summer
    interface type
      function declaration
        parameter
          reference type
            type name Self
        declared name sum
        type name i32
      function declaration
        parameter
          declared name self
          reference type
            type name Self
        declared name add
        declared name T
        parameter
          declared name value
          type name T
  type alias declaration
    declared name Alias
    declared name T
    optional type
      type name T
"#;
    assert!(messages(text).is_empty());
    assert_eq!(dump_of(text), want);
}

/// The real prelude, the first file of every program, parses cleanly.
#[test]
fn parses_the_prelude() {
    let path = concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/../../../_builtin_library/core/src/global.peep"
    );
    let text = std::fs::read_to_string(path).unwrap();
    assert!(messages(&text).is_empty());
}

#[test]
fn declaration_errors() {
    for (text, message) in [
        ("let a = 1;", "top-level `let` not allowed"),
        ("a + 1;", "module scope expects declaration"),
        (
            "#[a, b] fn f();",
            "only one attribute is allowed per `#[...]` block",
        ),
        (
            "iface I { fn (&Self) f(a: i32 = 1) }",
            "interface method defaults are not supported",
        ),
        ("iface I { fn f() }", "expected `(`, found `identifier`"),
        ("type A i32;", "expected `=`, found `identifier`"),
        ("struct S { a i32 }", "expected `:`, found `identifier`"),
        ("enum E { A B }", "expected `,`"),
    ] {
        one_error(text, message, None);
    }
}

/// A `///` on the line of earlier code is not the next declaration's
/// documentation.
#[test]
fn doc_comment_after_code() {
    assert!(!dump_of("const a = 1; /// about a\nconst b = 2;").contains("metadata"));
}
