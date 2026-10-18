//! The command line: what `peeper` is asked to do, and the usage text shown
//! when the arguments make no sense.

use std::path::PathBuf;

use crate::dump::Dump;

pub const USAGE: &str = "\
usage: peeper check [project folder | file.peep]
       peeper fix [project folder | file.peep]
       peeper -dump [thir | mir] [project folder | file.peep]
       peeper lsp
  check  report the program's errors
  fix    apply the fixes the errors suggest, then report what is left
  -dump  also write each module's THIR and MIR, or only the one named, to
         build/debug/dumps beside the project or file, when there are no errors
  lsp    serve editors as a language server over stdin and stdout";

/// What the arguments ask for.
pub enum Args {
    Check {
        path: PathBuf,    // the project folder or file
        dumps: Vec<Dump>, // what to dump; none for `check` and `fix`
        fix: bool,        // apply the suggested fixes first
    },
    Lsp,
}

/// The arguments after the program's name; `None` when they make no sense.
pub fn parse(args: &[String]) -> Option<Args> {
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let kind = |mode: &str| Dump::ALL.into_iter().find(|kind| kind.extension() == mode);
    let (dumps, path, fix): (Vec<Dump>, &str, bool) = match args.as_slice() {
        ["lsp"] => return Some(Args::Lsp),
        ["check"] => (Vec::new(), ".", false),
        ["check", path] => (Vec::new(), path, false),
        ["fix"] => (Vec::new(), ".", true),
        ["fix", path] => (Vec::new(), path, true),
        ["-dump"] => (Dump::ALL.to_vec(), ".", false),
        ["-dump", mode] if kind(mode).is_some() => (kind(mode).into_iter().collect(), ".", false),
        ["-dump", path] => (Dump::ALL.to_vec(), path, false),
        ["-dump", mode, path] if kind(mode).is_some() => {
            (kind(mode).into_iter().collect(), path, false)
        }
        _ => return None,
    };
    Some(Args::Check {
        path: PathBuf::from(path),
        dumps,
        fix,
    })
}

#[cfg(test)]
mod tests {
    use super::Args;
    use super::parse;

    /// The path and dump extensions `args` ask for, with "fix" added for
    /// `peeper fix`, `("lsp", [])` for `peeper lsp`, or `None`.
    fn parsed(args: &[&str]) -> Option<(String, Vec<&'static str>)> {
        let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
        match parse(&args)? {
            Args::Check { path, dumps, fix } => {
                let mut asked: Vec<&str> = dumps.iter().map(|dump| dump.extension()).collect();
                if fix {
                    asked.push("fix");
                }
                Some((path.display().to_string(), asked))
            }
            Args::Lsp => Some(("lsp".to_string(), Vec::new())),
        }
    }

    #[test]
    fn commands() {
        let some = |path: &str, dumps: &[&'static str]| Some((path.to_string(), dumps.to_vec()));
        assert_eq!(parsed(&["check"]), some(".", &[]));
        assert_eq!(parsed(&["check", "app"]), some("app", &[]));
        assert_eq!(parsed(&["fix"]), some(".", &["fix"]));
        assert_eq!(parsed(&["fix", "app"]), some("app", &["fix"]));
        assert_eq!(parsed(&["-dump"]), some(".", &["thir", "mir"]));
        assert_eq!(parsed(&["-dump", "mir"]), some(".", &["mir"]));
        assert_eq!(parsed(&["-dump", "app"]), some("app", &["thir", "mir"]));
        assert_eq!(parsed(&["-dump", "thir", "app"]), some("app", &["thir"]));
        assert_eq!(parsed(&["lsp"]), some("lsp", &[]));
        for wrong in [
            &[][..],
            &["build"],
            &["-dump", "ll", "app"],
            &["check", "a", "b"],
            &["fix", "a", "b"],
            &["lsp", "app"],
        ] {
            assert_eq!(parsed(wrong), None, "{wrong:?}");
        }
    }
}
