use super::*;
use crate::Code;

fn span(file: &SourceFile, text: &str) -> Span {
    let start = file.text.find(text).unwrap();
    Span::new(start, start + text.len())
}

fn render(diagnostics: &[Diagnostic]) -> String {
    let mut out = Vec::new();
    print(&mut out, diagnostics, false).unwrap();
    String::from_utf8(out).unwrap()
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

#[test]
fn help_with_fix() {
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
  = help: remove this comma
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
    print(&mut out, &[d], true).unwrap();
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
        got.contains("[L0001]: outside") && got.contains("= help: broken fix is not drawn"),
        "{got}"
    );
}

#[test]
fn color() {
    let file = Arc::new(SourceFile::new("a.peep", "x\n"));
    let d =
        Diagnostic::new(code("W0001", Severity::Warning), "w").label(&file, span(&file, "x"), "");
    let mut out = Vec::new();
    print(&mut out, &[d], true).unwrap();
    let out = String::from_utf8(out).unwrap();
    assert!(out.contains("\x1b[1;33m[W0001]\x1b[0m"), "{out:?}");
}
