use std::convert::Infallible;

use syntax::ast::Decl;
use syntax::ast::Field;
use syntax::ast::StringKind;
use syntax::ast::Type;
use syntax::ast::WrittenRange;
use syntax::operators::Precedence;

use super::*;

/// The language the parser's tests are written in: the core, with no
/// feature, since the `features` crate depends on this one.
#[derive(Clone, Copy, Debug)]
pub(crate) enum Bare {}

impl Language for Bare {
    type Expr = Infallible;
    type Type = Infallible;
    type Stmt = Infallible;

    fn precedence(expr: &Infallible) -> Precedence {
        match *expr {}
    }

    fn fields(ty: &Infallible) -> Option<List<Field<Bare>>> {
        match *ty {}
    }

    fn methods(ty: &Infallible) -> Option<List<Decl<Bare>>> {
        match *ty {}
    }

    fn pointee(ty: &Infallible) -> Option<Id<Type<Bare>>> {
        match *ty {}
    }

    fn element(ty: &Infallible) -> Option<Id<Type<Bare>>> {
        match *ty {}
    }

    fn string_kind(expr: &Infallible) -> Option<StringKind> {
        match *expr {}
    }

    fn written_range(expr: &Infallible) -> Option<WrittenRange<Bare>> {
        match *expr {}
    }
}

static RULES: Rules<Bare> = Rules::core();

impl Grammar for Bare {
    fn rules() -> &'static Rules<Bare> {
        &RULES
    }
}

pub(crate) fn parse_text(text: &str) -> (Tree<Bare>, Vec<Diagnostic>) {
    parse(&Arc::new(SourceFile::new("t.peep", text)))
}

/// The tree of `text` in the dump format.
pub(crate) fn dump_of(text: &str) -> String {
    dump::dump(&parse_text(text).0, false)
}

/// The error messages for `text`.
pub(crate) fn messages(text: &str) -> Vec<String> {
    parse_text(text).1.into_iter().map(|e| e.message).collect()
}

/// `text` with the fixes of the first help of `d` applied.
pub(crate) fn fixed(text: &str, d: &Diagnostic) -> String {
    let mut text = text.to_string();
    for fix in d.notes[0].fixes().iter().rev() {
        text.replace_range(fix.span.range(), &fix.new_text);
    }
    text
}

/// Checks that `text` gives exactly one error, `message`, and that its fix
/// (when `want_fixed` is given) gives `want_fixed`.
pub(crate) fn one_error(text: &str, message: &str, want_fixed: Option<&str>) {
    let (_, errors) = parse_text(text);
    let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
    assert_eq!(got, [message], "{text:?}");
    if let Some(want) = want_fixed {
        assert_eq!(fixed(text, &errors[0]), want, "{text:?}");
    }
}

pub(crate) const PROGRAM: &str = r#"import "app/util" as u;
import "core:io";

fn add(a: i32, b: i32) -> i32 {
    let sum: i32 = a + b * 2;
    return sum;
}

fn main() {
    let x = add(1, (2 + 3)) == 7;
    log(x);
    return;
}
"#;

#[test]
fn parses_a_program() {
    let want = r#"module
  import declaration
    string literal "app/util"
    declared name u
  import declaration
    string literal "core:io"
  function declaration
    declared name add
    parameter
      declared name a
      type name i32
    parameter
      declared name b
      type name i32
    type name i32
    block
      let statement
        declared name sum
        type name i32
        binary expression +
          name reference a
          binary expression *
            name reference b
            number literal 2
      return statement
        name reference sum
  function declaration
    declared name main
    block
      let statement
        declared name x
        binary expression ==
          call expression
            name reference add
            number literal 1
            parentheses
              binary expression +
                number literal 2
                number literal 3
          number literal 7
      expression statement
        call expression
          name reference log
          name reference x
      return statement return;
"#;
    assert!(messages(PROGRAM).is_empty());
    assert_eq!(dump_of(PROGRAM), want);
}

#[test]
fn recovers_after_a_missing_semicolon() {
    let text = "fn f() { let a = 1 let b = 2; return b; }";
    let errors = messages(text);
    assert!(
        errors.len() == 1 && errors[0].contains("expected `;`"),
        "{errors:?}"
    );
    assert!(dump_of(text).contains("return statement\n        name reference b\n"));
}

#[test]
fn underscore_binds_nothing() {
    let text = "fn f(_: i32) {
    for _, v in xs {}
    let .{ b, _ } = s;
}";
    assert_eq!(messages(text), Vec::<String>::new());
    let dump = dump_of(text);
    assert!(dump.contains("rest pattern"), "{dump}");
    assert!(!dump.contains("declared name _"), "{dump}");
    for text in [
        "fn f() { let _ = 1; }",
        "const _: i32 = 2;",
        "fn _() {}",
        "fn f() { let .{ a as _ } = s; }",
    ] {
        one_error(text, "expected `identifier`, found `_`", None);
    }
    for text in ["fn f() { let x = _; }", "fn f() { _ = 1; }"] {
        one_error(text, "expected expression, found `_`", None);
    }
}

#[test]
fn one_error_for_unexpected_top_level_text() {
    let text = "let b = 1;\nx + 1; y();\nfn f() {}";
    assert_eq!(
        messages(text),
        [
            "top-level `let` not allowed",
            "module scope expects declaration"
        ]
    );
    assert_eq!(
        dump_of(text),
        "module\n  function declaration\n    declared name f\n    block {}\n"
    );
}

/// Inputs with errors in many places; parsing them must still give a tree
/// that keeps its rules.
const BROKEN_INPUTS: &[&str] = &[
    "",
    "fn",
    "fn f(",
    "fn f(a: ) {",
    "fn f() { let = ; }",
    "fn f() { return 1 + ; }",
    "fn f() { g(1, ; }",
    "import ;",
    "import \"x\" as ;",
    "let x = 1;",
    "fn f() -> { }",
    "fn f() { ((1 }",
    "fn f( {",
    "fn f() { a$b; }",
    "fn f() { g(1 2); }",
    "fn f(a: i32 b: i32) {}",
    "fn f() { fn }",
    "fn f() { ) ; let x = ; }",
    "fn f() { let a = 1\nfn g() {}",
    "fn f() { g(a b c); h(1 2); }",
    "fn (",
    "fn (a: &) f",
    "fn f<",
    "fn f<T",
    "fn f(a: Box<T) {}",
    "fn f(a: Box<Box<T>) {}",
    "fn f(a: [",
    "fn f(a: [4",
    "fn f(a: fn(",
    "fn f() -> &mut",
    "fn f() -> i32 in (a",
    "fn f(a: ??) {}",
    "fn f(a: a::) {}",
    "fn f(mut mut) {}",
    "fn f() { a.; }",
    "fn f() { a[; }",
    "fn f() { a[1..=]; }",
    "fn f() { a as ; }",
    "fn f() { [_]i32{1, 2; }",
    "fn f() { [x]i32{}; }",
    "fn f() { Box::<T>::; }",
    "fn f() { &mut ; }",
    "fn f() { a < b > c; }",
    "fn f() { -; }",
    "fn f() { if }",
    "fn f() { if a }",
    "fn f() { if a {} else }",
    "fn f() { if a = b {} }",
    "fn f() { for }",
    "fn f() { for x in }",
    "fn f() { for i, in x {} }",
    "fn f() { for a.b in x {} }",
    "fn f() { break l; }",
    "fn f() { let .{ a = b, _, c } = v; }",
    "fn f() { let .{ } }",
    "fn f() { a = ; }",
    "fn f() { let mut ; }",
    "fn f() { const c = 1; { } }",
    "///",
    "/// x",
    "#",
    "#[",
    "#[a(",
    "#[a(1, ] fn f();",
    "const",
    "const a",
    "const a: = ;",
    "type",
    "type A =",
    "type A<T",
    "fn f() { let a: i32; }",
    "/// doc\n#[a] let x = 1;",
];

/// Every span lies in the text and starts before it ends. (A child need not
/// lie inside its parent: an empty error node sits where reading stopped,
/// which can be just before the statement that holds it, as in Go.)
#[test]
fn spans_lie_in_the_text() {
    for text in std::iter::once(&PROGRAM).chain(BROKEN_INPUTS) {
        let (tree, _) = parse_text(text);
        for line in dump::dump(&tree, true).lines() {
            // the span follows the kind; a doc comment's later lines have none
            let Some(at) = line.find(" @") else { continue };
            let span = line[at + 2..].split(' ').next().unwrap();
            let (start, end) = span.split_once('-').unwrap();
            let (start, end): (usize, usize) = (start.parse().unwrap(), end.parse().unwrap());
            assert!(start <= end && end <= text.len(), "{text:?}: {line}");
        }
    }
}

/// Each mistake is reported once, and parsing goes on to report the next.
#[test]
fn one_error_per_mistake() {
    for (text, want) in [
        ("fn f( {", &["expected `)`, found `{`"][..]),
        ("fn", &["expected `identifier`, found `end of file`"]),
        ("fn f() { ((1 }", &["expected `)`, found `}`"]),
        ("fn f() { g(1, ; }", &["expected `)`, found `;`"]),
        (
            "fn f() { return 1 + ; }",
            &["expected expression, found `;`"],
        ),
        ("fn f() { g(1 2); }", &["expected `,`"]),
        ("fn f(a: i32 b: i32) {}", &["expected `,`"]),
        ("fn f(_) {}", &["expected `:`, found `)`"]),
        (
            "fn f() {}\n#[a]",
            &["an attribute needs a declaration after it"],
        ),
        (
            "#[a] import \"x:y\";",
            &["an attribute needs a declaration after it"],
        ),
        ("fn f(g: fn(_, _)) {}", &["expected `:`, found `,`"]),
        ("fn f() { a$b; }", &["illegal character \"$\""]), // the parser adds nothing
        (
            "fn f() { let a = ; let b = ; }",
            &[
                "expected expression, found `;`",
                "expected expression, found `;`",
            ],
        ),
        (
            "fn f() { ) ; let x = ; }",
            &[
                "expected expression, found `)`",
                "expected expression, found `;`",
            ],
        ),
        (
            "fn f() { g(a b c); h(1 2); }",
            &["expected `,`", "expected `,`"],
        ),
        (
            "fn f() { let a = 1\nfn g() {}", // the `;` and the `}`
            &["expected `;`, found `fn`", "expected `}`, found `fn`"],
        ),
    ] {
        assert_eq!(messages(text), want, "{text:?}");
    }
}

/// A `;` missing at a line end before the next statement is read past and
/// leaves the tree complete; one missing inside a line makes the parser
/// skip, and an illegal character drops text, so neither tree is.
#[test]
fn complete_trees() {
    for (text, complete) in [
        ("fn f() {\n    let a = 1\n    let b = 2\n}\n", true),
        ("fn f() { let a = 1 let b = 2; }", false),
        ("fn f() { a $ 1; }", false),
        ("fn f() { let a = 1; }", true),
    ] {
        let (tree, errors) = parse_text(text);
        assert_eq!(tree.complete, complete, "{text:?} {errors:?}");
    }
}

/// Items without commas are read as if the commas were there, with one fix
/// per missing comma.
#[test]
fn missing_commas_are_repaired() {
    let text = "fn f(a: i32 b: i32) { g(a b c); }";
    let want = "module
  function declaration
    declared name f
    parameter
      declared name a
      type name i32
    parameter
      declared name b
      type name i32
    block
      expression statement
        call expression
          name reference g
          name reference a
          name reference b
          name reference c
";
    assert_eq!(dump_of(text), want);
    let (_, errors) = parse_text(text);
    assert_eq!(errors.len(), 2);
    assert_eq!(errors[0].notes[0].fixes().len(), 1);
    assert_eq!(errors[1].notes[0].fixes().len(), 2);
    let second = errors[1].notes[0].fixes()[1].span.start as usize;
    assert_eq!(&text[..second], "fn f(a: i32 b: i32) { g(a b");
}

/// A missing closer on a later line points back at what is still open.
#[test]
fn unclosed_marks_the_opener() {
    let (_, errors) = parse_text("fn f() {\n    g(1;\n}");
    assert!(
        errors.len() == 1 && errors[0].labels.len() == 1,
        "same line: {errors:?}"
    );
    let (_, errors) = parse_text("fn f() {\n    let a = 1;\n");
    assert!(
        errors.len() == 1
            && errors[0].labels.len() == 2
            && errors[0].labels[1].message == "this `{` is still open",
        "later line: {errors:?}"
    );
}

/// Where a missing token is reported: at the end of the text read when the
/// next token is on a later line, with a fix that inserts it there; at the
/// token found when it is on the same line.
#[test]
fn missing_token_place() {
    one_error(
        "fn f() {\n    let a = 1\n    return a;\n}",
        "expected `;`, found `return`",
        Some("fn f() {\n    let a = 1;\n    return a;\n}"),
    );
    let text = "fn f() { let a = 1 let b = 2; }";
    let (_, errors) = parse_text(text);
    assert!(
        errors.len() == 1 && errors[0].notes.is_empty(),
        "{errors:?}"
    );
    assert_eq!(&text[errors[0].labels[0].span.range()], "let");
    // A missing closer always belongs where the read text ends.
    let text = "fn f() { g(1 + 2; }";
    let (_, errors) = parse_text(text);
    assert_eq!(errors.len(), 1);
    assert_eq!(
        errors[0].notes[0].fixes()[0].span.start as usize,
        text.find(';').unwrap()
    );
}

/// What the parser accepts and repairs between items: extra commas,
/// trailing commas and empty statements are style, left to a lint.
#[test]
fn separators() {
    assert!(messages("fn f(a: i32,, b: i32,) { g(1,, 2,); ;; }").is_empty());
}

/// Nesting far deeper than the thread's stack would hold still parses:
/// the nesting rules move to new stack segments as they need them.
#[test]
fn deep_nesting_does_not_overflow() {
    let n = 100_000;
    for text in [
        format!("fn f() {{ let a = {}1{}; }}", "(".repeat(n), ")".repeat(n)),
        format!("fn f() {}{}", "{".repeat(n), "}".repeat(n)),
        format!("fn f() {{ if a {{ }}{} }}", " else if a { }".repeat(n)),
        format!("fn f(a: {}i32) {{}}", "& ".repeat(n)),
    ] {
        let (tree, _) = parse_text(&text);
        assert_eq!(tree.top_level.len(), 1);
    }
}
