//! The Peeper compiler command.

use std::io::{IsTerminal, Write};
use std::process::ExitCode;
use std::sync::Arc;

use diagnostics::{Diagnostic, Severity, printer};
use source::{SourceFile, Span};

const USAGE: &str = "usage: peeper check file.peep";

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let [command, path] = args.as_slice() else {
        eprintln!("{USAGE}");
        return ExitCode::from(2);
    };
    if command != "check" {
        eprintln!("{USAGE}");
        return ExitCode::from(2);
    }
    let stderr = std::io::stderr();
    let colored = stderr.is_terminal();
    ExitCode::from(check(path, &mut stderr.lock(), colored))
}

/// Checks the file at `path`, prints its diagnostics to `out` and returns the
/// exit code: 1 when there are errors.
// shortcut: one file, lexer only; project folders and parsing come with the
// project loader and the parser.
fn check(path: &str, out: &mut impl Write, colored: bool) -> u8 {
    let bytes = match std::fs::read(path) {
        Ok(bytes) => bytes,
        Err(err) => {
            _ = writeln!(out, "{path}: {err}");
            return 1;
        }
    };
    let errors = match String::from_utf8(bytes) {
        Ok(text) => lexer::scan(&Arc::new(SourceFile::new(path, text))).1,
        Err(err) => {
            // The text up to the bad byte is kept as is, so the error points at it.
            let at = err.utf8_error().valid_up_to();
            let text = String::from_utf8_lossy(err.as_bytes()).into_owned();
            let file = Arc::new(SourceFile::new(path, text));
            let bad = Span::new(at, at + char::REPLACEMENT_CHARACTER.len_utf8());
            let error =
                Diagnostic::new(diagnostics::UNEXPECTED_CHARACTER, "file is not valid UTF-8")
                    .label(&file, bad, "this byte is not UTF-8")
                    .note("Peeper source files must be UTF-8 text");
            vec![error]
        }
    };
    let scan = |file: &Arc<SourceFile>| lexer::scan(file).0;
    let highlight: Option<printer::TokensOf> = if colored { Some(&scan) } else { None };
    if let Err(err) = printer::print(out, &errors, highlight) {
        eprintln!("peeper: cannot write diagnostics: {err}");
        return 1;
    }
    u8::from(errors.iter().any(|d| d.code.severity == Severity::Error))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn check_reports_and_sets_the_exit_code() {
        let dir = std::env::temp_dir().join(format!("peeper-check-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let bad = dir.join("bad.peep");
        let good = dir.join("good.peep");
        std::fs::write(&bad, "fn main() { a $ b; }\n").unwrap();
        std::fs::write(&good, "fn main() {}\n").unwrap();

        let mut out = Vec::new();
        assert_eq!(check(bad.to_str().unwrap(), &mut out, false), 1);
        let out = String::from_utf8(out).unwrap();
        assert!(
            out.starts_with("[L0001]: illegal character \"$\"\n"),
            "{out}"
        );
        assert!(
            out.ends_with("Compilation failed with 1 error(s)\n"),
            "{out}"
        );

        let mut out = Vec::new();
        assert_eq!(check(good.to_str().unwrap(), &mut out, false), 0);
        assert!(out.is_empty());

        let invalid = dir.join("invalid.peep");
        std::fs::write(&invalid, b"fn f() {}\n// \xff\n").unwrap();
        let mut out = Vec::new();
        assert_eq!(check(invalid.to_str().unwrap(), &mut out, false), 1);
        let out = String::from_utf8(out).unwrap();
        assert!(
            out.starts_with("[L0001]: file is not valid UTF-8\n"),
            "{out}"
        );
        assert!(out.contains(":2:4\n"), "points at the bad byte: {out}");

        let mut out = Vec::new();
        assert_eq!(
            check(dir.join("missing.peep").to_str().unwrap(), &mut out, false),
            1
        );
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
