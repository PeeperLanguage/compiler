use super::*;
use source::Span;
use token::Token;

use crate::Code;

fn span(file: &SourceFile, text: &str) -> Span {
    let start = file.text.find(text).unwrap();
    Span::new(start, start + text.len())
}

fn render(diagnostics: &[Diagnostic]) -> String {
    let mut out = Vec::new();
    print(&mut out, diagnostics, None).unwrap();
    String::from_utf8(out).unwrap()
}

fn no_tokens(_: &Arc<SourceFile>) -> Vec<Token> {
    Vec::new()
}

fn code(id: &'static str, severity: Severity) -> Code {
    Code { id, severity }
}

#[test]
fn two_files() {
    let main = Arc::new(SourceFile::new(
        "src/main.peep",
        "fn main() -> i32 {\n    global::exit(0);\n}\n",
    ));
    let global = Arc::new(SourceFile::new(
        "lib/global.peep",
        "#[extern(\"exit\")]\nfn exit(code: i32);\n",
    ));
    let d = Diagnostic::new(
        code("M0004", Severity::Error),
        "`exit` is not exported from `global`",
    )
    .label(
        &main,
        span(&main, "global::exit"),
        "use of unexported symbol",
    )
    .label(
        &global,
        {
            let s = span(&global, "exit(");
            Span::new(s.start as usize, s.end as usize - 1)
        },
        "defined here",
    )
    .note("symbols with uppercase are exported otherwise private");
    let want = "[M0004]: `exit` is not exported from `global`
  --> src/main.peep:2:5
  | 
1 | fn main() -> i32 {
2 |     global::exit(0);
  |     ^^^^^^^^^^^^ use of unexported symbol

  --> lib/global.peep:2:4
  | 
1 | #[extern(\"exit\")]
2 | fn exit(code: i32);
  |    ---- defined here
  | 
  = note: symbols with uppercase are exported otherwise private

Compilation failed with 1 error(s)
";
    assert_eq!(render(&[d]), want);
}

/// A sure fix is labeled `fix`; a guess and plain advice stay `help`.
#[test]
fn fix_and_help_labels() {
    let file = Arc::new(SourceFile::new("a.peep", "cout\n"));
    let at = span(&file, "cout");
    let fix = Fix {
        file: file.clone(),
        span: at,
        new_text: "count".into(),
    };
    let d = Diagnostic::new(code("T0002", Severity::Error), "unknown name `cout`")
        .label(&file, at, "")
        .guess("did you mean `count`?", vec![fix])
        .help("declare it first", Vec::new());
    let got = render(&[d]);
    assert!(got.contains("= help: did you mean `count`?"), "{got}");
    assert!(got.contains("= help: declare it first"), "{got}");
    assert!(!got.contains("= fix:"), "{got}");
}

#[test]
fn fix_with_preview() {
    let file = Arc::new(SourceFile::new(
        "a.peep",
        "struct S {\n    value: i32,\n}\n",
    ));
    let comma = span(&file, ",");
    let fix = Fix {
        file: file.clone(),
        span: comma,
        new_text: String::new(),
    };
    let d = Diagnostic::new(
        code("S0001", Severity::Info),
        "trailing comma is unnecessary",
    )
    .label(&file, comma, "")
    .help("remove this comma", vec![fix]);
    let want = "[S0001]: trailing comma is unnecessary
  --> a.peep:2:15
  | 
1 | struct S {
2 |     value: i32,
  |               ^
  | 
  = fix: remove this comma
  | 
2 |     value: i32

";
    assert_eq!(render(&[d]), want);
}

#[test]
fn inserted_text_is_marked() {
    let file = Arc::new(SourceFile::new("a.peep", "let a = 1\n"));
    let at = Span::at(9);
    let fix = Fix {
        file: file.clone(),
        span: at,
        new_text: ";".into(),
    };
    let d = Diagnostic::new(code("P0002", Severity::Error), "expected `;`")
        .label(&file, at, "")
        .help("add missing `;`", vec![fix]);
    let mut out = Vec::new();
    print(&mut out, &[d], Some(&no_tokens)).unwrap();
    let out = String::from_utf8(out).unwrap();
    assert!(
        out.contains("let a = 1\x1b[38;5;22;48;5;114m;\x1b[0m\n"),
        "{out:?}"
    );
}

#[test]
fn order_and_summary() {
    let file = Arc::new(SourceFile::new("a.peep", "a\nb\n"));
    let late = Diagnostic::new(code("W0001", Severity::Warning), "late").label(
        &file,
        span(&file, "b"),
        "",
    );
    let early =
        Diagnostic::new(code("L0001", Severity::Error), "early").label(&file, span(&file, "a"), "");
    let nowhere = Diagnostic::new(code("M0001", Severity::Error), "no place");
    let got = render(&[late, early, nowhere]);
    let mut last = 0;
    for part in [
        "[M0001]",
        "[L0001]",
        "[W0001]",
        "Compilation failed with 2 error(s) and 1 warning(s)",
    ] {
        let at = got
            .find(part)
            .unwrap_or_else(|| panic!("{part} missing in:\n{got}"));
        assert!(at >= last, "{part} out of order in:\n{got}");
        last = at;
    }
}

#[test]
fn span_over_lines_tabs_and_wide_characters() {
    let file = Arc::new(SourceFile::new("a.peep", "\tlet 名 = (\r\n  1)\r\n"));
    let start = file.text.find('名').unwrap();
    let end = file.text.find(')').unwrap() + 1;
    let d = Diagnostic::new(code("P0003", Severity::Error), "bad").label(
        &file,
        Span::new(start, end),
        "here",
    );
    let want = "[P0003]: bad
  --> a.peep:1:6
  | 
1 |     let 名 = (
  |         ^^^^^^
2 |   1)
  | ^^^^ here

Compilation failed with 1 error(s)
";
    assert_eq!(render(&[d]), want);
}

/// A message that would run into the next mark drops below it, the
/// rightmost first, so no `|` crosses a message.
#[test]
fn marks_side_by_side() {
    let file = Arc::new(SourceFile::new(
        "a.peep",
        "let n = add(width, height) + scale(depth);\n",
    ));
    let d = Diagnostic::new(code("T0001", Severity::Error), "mismatched types")
        .label(&file, span(&file, "depth"), "expected `i32`, found `bool`")
        .label(&file, span(&file, "width"), "`width` is `i32`")
        .label(&file, span(&file, "height"), "`height` is `i64`");
    let want = "[T0001]: mismatched types
  --> a.peep:1:36
  |\x20
1 | let n = add(width, height) + scale(depth);
  |             -----  ------          ^^^^^ expected `i32`, found `bool`
  |             |      |
  |             |      ------ `height` is `i64`
  |             ----- `width` is `i32`

Compilation failed with 1 error(s)
";
    assert_eq!(render(&[d]), want);
}

/// A mark inside a longer one goes above the line; the longer one shows
/// the word it starts with, then drops to show in full.
#[test]
fn marks_inside_marks() {
    let file = Arc::new(SourceFile::new(
        "a.peep",
        "return wrap(P.{ x = f(1), y = g(2) });\n",
    ));
    let d = Diagnostic::new(code("T0001", Severity::Error), "mismatched types")
        .label(
            &file,
            span(&file, "wrap(P.{ x = f(1), y = g(2) })"),
            "`wrap` takes `Q`",
        )
        .label(
            &file,
            span(&file, "P.{ x = f(1), y = g(2) }"),
            "`P` literal",
        )
        .label(&file, span(&file, "f(1)"), "`f` returns `bool`")
        .label(&file, span(&file, "g(2)"), "`g` returns `u8`");
    let want = "[T0001]: mismatched types
  --> a.peep:1:8
  |\x20
  |                     ---- `f` returns `bool`
  |                     |
  |                     ----      ---- `g` returns `u8`
1 | return wrap(P.{ x = f(1), y = g(2) });
  |        ^^^^ ------------------------ `P` literal
  |        |
  |        ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^ `wrap` takes `Q`

Compilation failed with 1 error(s)
";
    assert_eq!(render(&[d]), want);
}

#[test]
fn never_fails_on_bad_spans() {
    let file = Arc::new(SourceFile::new("a.peep", "x\n"));
    let fix = Fix {
        file: file.clone(),
        span: Span::new(1, 99),
        new_text: "y".into(),
    };
    let d = Diagnostic::new(code("L0001", Severity::Error), "outside")
        .label(&file, Span::new(40, 2), "")
        .help("broken fix is not drawn", vec![fix]);
    let got = render(&[d]);
    assert!(
        got.contains("[L0001]: outside") && got.contains("= fix: broken fix is not drawn"),
        "{got}"
    );
}

#[test]
fn marks_far_into_a_long_line() {
    let text = format!("{}x\n", " ".repeat(70_000));
    let file = Arc::new(SourceFile::new("a.peep", text));
    let d = Diagnostic::new(code("L0001", Severity::Error), "far")
        .label(&file, Span::new(70_000, 70_001), "a")
        .label(&file, Span::new(70_000, 70_001), "b");
    assert!(render(&[d]).contains("^ a\n"));
}

#[test]
fn color() {
    let file = Arc::new(SourceFile::new("a.peep", "x\n"));
    let d =
        Diagnostic::new(code("W0001", Severity::Warning), "w").label(&file, span(&file, "x"), "");
    let mut out = Vec::new();
    print(&mut out, &[d], Some(&no_tokens)).unwrap();
    let out = String::from_utf8(out).unwrap();
    assert!(out.contains("\x1b[1;33m[W0001]\x1b[0m"), "{out:?}");
}

#[test]
fn source_lines_are_highlighted_in_color() {
    let text = "fn main() {\n    let s = \"x\" + 1; // note\n}\n";
    let file = Arc::new(SourceFile::new("a.peep", text));
    let d =
        Diagnostic::new(code("P0003", Severity::Error), "bad").label(&file, span(&file, "+"), "");
    // The lexer's tokens of `text`, comment left out as the lexer leaves it.
    let tokens = |file: &Arc<SourceFile>| {
        use token::TokenKind;
        let mut at = 0;
        let parts = [
            ("fn", TokenKind::Fn),
            ("main", TokenKind::Ident),
            ("(", TokenKind::LeftParen),
            (")", TokenKind::RightParen),
            ("{", TokenKind::LeftBrace),
            ("let", TokenKind::Let),
            ("s", TokenKind::Ident),
            ("=", TokenKind::Assign),
            ("\"x\"", TokenKind::String),
            ("+", TokenKind::Plus),
            ("1", TokenKind::Number),
            (";", TokenKind::Semicolon),
            ("}", TokenKind::RightBrace),
        ];
        let mut tokens: Vec<Token> = parts
            .iter()
            .map(|&(part, kind)| {
                let start = at + file.text[at..].find(part).unwrap();
                at = start + part.len();
                Token {
                    kind,
                    span: Span::new(start, at),
                }
            })
            .collect();
        tokens.push(Token {
            kind: TokenKind::Eof,
            span: Span::at(file.text.len()),
        });
        tokens
    };
    let mut out = Vec::new();
    print(&mut out, &[d], Some(&tokens)).unwrap();
    let out = String::from_utf8(out).unwrap();
    let line = "    \x1b[35mlet\x1b[0m s = \x1b[38;5;120m\"x\"\x1b[0m + \x1b[38;5;215m1\x1b[0m; \x1b[90m// note\x1b[0m\n";
    assert!(out.contains(line), "{out:?}");
    assert!(
        out.contains("\x1b[35mfn\x1b[0m main() {\n"),
        "context line: {out:?}"
    );
    assert!(
        !render(&[
            Diagnostic::new(code("P0003", Severity::Error), "bad").label(
                &file,
                span(&file, "+"),
                ""
            )
        ])
        .contains('\x1b')
    );
}
