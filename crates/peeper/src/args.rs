//! The command line: what `peeper` is asked to do, and the usage text shown
//! when the arguments make no sense.

use std::path::PathBuf;

use crate::dump::Dump;

pub const USAGE: &str = "\
usage: peeper check [project folder | file.peep]
       peeper fix [project folder | file.peep]
       peeper build [-o program] [project folder | file.peep]
       peeper run [project folder | file.peep] [arguments...]
       peeper -dump [thir | mir | ll] [project folder | file.peep]
       peeper init [name]
       peeper lsp
  check  report the program's errors
  fix    apply the fixes the errors suggest, then report what is left
  build  check the program and link it into an executable, named after the
         project or file unless -o names it
  run    build the program to a temporary file and run it with the arguments
  -dump  also write each module's THIR, MIR and LLVM IR, or only the one named, to
         build/debug/dumps beside the project or file, when there are no errors
  init   make this folder a project: write peeper.toml and src/main.peep,
         asking for the name unless given
  lsp    serve editors as a language server over stdin and stdout";

/// What the arguments ask for.
pub enum Args {
    Check {
        path: PathBuf,    // the project folder or file
        dumps: Vec<Dump>, // what to dump; none for `check` and `fix`
        fix: bool,        // apply the suggested fixes first
    },
    Build {
        path: PathBuf,
        output: Option<PathBuf>, // the executable's path, from -o
    },
    Run {
        path: PathBuf,
        arguments: Vec<String>, // for the program
    },
    Init {
        name: Option<String>, // asked for when not given
    },
    Lsp,
}

/// The arguments after the program's name; `None` when they make no sense.
pub fn parse(args: &[String]) -> Option<Args> {
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let kind = |mode: &str| Dump::ALL.into_iter().find(|kind| kind.extension() == mode);
    let (dumps, path, fix): (Vec<Dump>, &str, bool) = match args.as_slice() {
        ["lsp"] => return Some(Args::Lsp),
        ["init"] => return Some(Args::Init { name: None }),
        ["init", name] => {
            return Some(Args::Init {
                name: Some(name.to_string()),
            });
        }
        ["build", rest @ ..] => {
            let (output, path) = match rest {
                [] => (None, "."),
                [path] if *path != "-o" => (None, *path),
                ["-o", output] => (Some(*output), "."),
                ["-o", output, path] => (Some(*output), *path),
                _ => return None,
            };
            return Some(Args::Build {
                path: PathBuf::from(path),
                output: output.map(PathBuf::from),
            });
        }
        ["run", rest @ ..] => {
            let (path, arguments) = rest.split_first().unwrap_or((&".", &[]));
            return Some(Args::Run {
                path: PathBuf::from(path),
                arguments: arguments
                    .iter()
                    .map(|argument| argument.to_string())
                    .collect(),
            });
        }
        ["check"] => (Vec::new(), ".", false),
        ["check", path] => (Vec::new(), path, false),
        ["fix"] => (Vec::new(), ".", true),
        ["fix", path] => (Vec::new(), path, true),
        ["-dump"] => (Dump::ALL.to_vec(), ".", false),
        ["-dump", mode] if let Some(dump) = kind(mode) => (vec![dump], ".", false),
        ["-dump", path] => (Dump::ALL.to_vec(), path, false),
        ["-dump", mode, path] if let Some(dump) = kind(mode) => (vec![dump], path, false),
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
    /// `peeper fix`, `("lsp", [])` for `peeper lsp`, `("init", [name])` for
    /// `peeper init`, or `None`. `build` and
    /// `run` give "build" or "run", then -o's value or the arguments.
    fn parsed(args: &[&str]) -> Option<(String, Vec<String>)> {
        let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
        match parse(&args)? {
            Args::Check { path, dumps, fix } => {
                let mut asked: Vec<String> = dumps
                    .iter()
                    .map(|dump| dump.extension().to_string())
                    .collect();
                if fix {
                    asked.push("fix".to_string());
                }
                Some((path.display().to_string(), asked))
            }
            Args::Build { path, output } => {
                let mut asked = vec!["build".to_string()];
                asked.extend(output.map(|output| output.display().to_string()));
                Some((path.display().to_string(), asked))
            }
            Args::Run { path, arguments } => {
                let asked = ["run".to_string()].into_iter().chain(arguments).collect();
                Some((path.display().to_string(), asked))
            }
            Args::Lsp => Some(("lsp".to_string(), Vec::new())),
            Args::Init { name } => Some(("init".to_string(), name.into_iter().collect())),
        }
    }

    #[test]
    fn commands() {
        let some = |path: &str, asked: &[&str]| {
            Some((
                path.to_string(),
                asked.iter().map(|a| a.to_string()).collect(),
            ))
        };
        assert_eq!(parsed(&["check"]), some(".", &[]));
        assert_eq!(parsed(&["check", "app"]), some("app", &[]));
        assert_eq!(parsed(&["fix"]), some(".", &["fix"]));
        assert_eq!(parsed(&["fix", "app"]), some("app", &["fix"]));
        assert_eq!(parsed(&["-dump"]), some(".", &["thir", "mir", "ll"]));
        assert_eq!(parsed(&["-dump", "mir"]), some(".", &["mir"]));
        assert_eq!(
            parsed(&["-dump", "app"]),
            some("app", &["thir", "mir", "ll"])
        );
        assert_eq!(parsed(&["-dump", "thir", "app"]), some("app", &["thir"]));
        assert_eq!(parsed(&["build"]), some(".", &["build"]));
        assert_eq!(parsed(&["build", "app"]), some("app", &["build"]));
        assert_eq!(
            parsed(&["build", "-o", "out"]),
            some(".", &["build", "out"])
        );
        assert_eq!(
            parsed(&["build", "-o", "out", "app"]),
            some("app", &["build", "out"])
        );
        assert_eq!(parsed(&["run"]), some(".", &["run"]));
        assert_eq!(
            parsed(&["run", "app", "-x", "y"]),
            some("app", &["run", "-x", "y"])
        );
        assert_eq!(parsed(&["lsp"]), some("lsp", &[]));
        assert_eq!(parsed(&["init"]), some("init", &[]));
        assert_eq!(parsed(&["init", "app"]), some("init", &["app"]));
        for wrong in [
            &[][..],
            &["build", "-o"],
            &["build", "a", "b"],
            &["-dump", "asm", "app"],
            &["check", "a", "b"],
            &["fix", "a", "b"],
            &["lsp", "app"],
            &["init", "a", "b"],
        ] {
            assert_eq!(parsed(wrong), None, "{wrong:?}");
        }
    }
}
