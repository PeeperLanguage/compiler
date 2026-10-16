//! The Peeper compiler command.

use std::io::{IsTerminal, Write};
use std::process::ExitCode;
use std::sync::Arc;

use diagnostics::Severity;
use source::SourceFile;

mod printer;

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
    let text = match std::fs::read_to_string(path) {
        Ok(text) => text,
        Err(err) => {
            _ = writeln!(out, "{path}: {err}");
            return 1;
        }
    };
    let file = Arc::new(SourceFile::new(path, text));
    let (_, errors) = lexer::scan(&file);
    if let Err(err) = printer::print(out, &errors, colored) {
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

        let mut out = Vec::new();
        assert_eq!(
            check(dir.join("missing.peep").to_str().unwrap(), &mut out, false),
            1
        );
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
