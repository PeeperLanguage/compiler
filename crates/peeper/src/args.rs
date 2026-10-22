//! The command line: what `peeper` is asked to do, and the usage text shown
//! when the arguments make no sense.

use std::path::PathBuf;

use llvm::BuildMode;

use crate::dump::Dump;

pub const USAGE: &str = "\
usage: peeper check [project folder | file.peep]
       peeper fix [project folder | file.peep]
       peeper build [-release] [-o program] [project folder | file.peep]
       peeper run [-release] [project folder | file.peep] [arguments...]
       peeper [check | fix] -dump [thir | mir | ll] [-release] [project folder | file.peep]
       peeper init [name]
       peeper lsp
  check  report the program's errors
  fix    apply the fixes the errors suggest, then report what is left
  build  check the program and link it into an executable, named after the
         project or file unless -o names it; -release builds it optimized,
         without debug info
  run    build the program to a temporary file and run it with the arguments
  -dump  also write each module's THIR, MIR and LLVM IR, or only the one named, to
         build/debug/dumps beside the project or file, when there are no errors;
         the LLVM IR of a debug build unless -release
  init   make this folder a project: write peeper.toml and src/main.peep,
         asking for the name unless given
  lsp    serve editors as a language server over stdin and stdout
Flags may come in any order, each with its value right after it.";

/// What the arguments ask for.
pub enum Args {
    Check {
        path: PathBuf,    // the project folder or file
        dumps: Vec<Dump>, // what to dump; none for `check` and `fix`
        fix: bool,        // apply the suggested fixes first
        mode: BuildMode,  // the build the LLVM IR dump is of; release with -release
    },
    Build {
        path: PathBuf,
        output: Option<PathBuf>, // the executable's path, from -o
        mode: BuildMode,         // release with -release, else debug
    },
    Run {
        path: PathBuf,
        arguments: Vec<String>, // for the program
        mode: BuildMode,
    },
    Init {
        name: Option<String>, // asked for when not given
    },
    Lsp,
}

/// The arguments after the program's name, or what is wrong with them.
/// Flags come anywhere, each with its value right after it: `-o` must have
/// one, `-dump` takes the next word when it names a dump. The words left,
/// in order, are the command and its value; `run` gives the words after its
/// path to the program. With no command, `-dump` means `check`.
pub fn parse(args: &[String]) -> Result<Args, String> {
    let kind = |name: &str| Dump::ALL.iter().position(|kind| kind.extension() == name);
    let mut output = None;
    let mut release = false;
    let mut dumps: Option<[bool; Dump::ALL.len()]> = None; // which were asked for, by -dump
    let mut words = Vec::new();
    let mut args = args.iter().map(String::as_str).peekable();
    while let Some(arg) = args.next() {
        match arg {
            "-o" if output.is_some() => return Err("-o is given twice".to_string()),
            "-o" => match args.next_if(|value| !value.starts_with('-')) {
                Some(value) => output = Some(PathBuf::from(value)),
                None => return Err("-o needs the program's path after it".to_string()),
            },
            "-release" if release => return Err("-release is given twice".to_string()),
            "-release" => release = true,
            "-dump" => {
                let asked = dumps.get_or_insert([false; Dump::ALL.len()]);
                match args.peek().and_then(|name| kind(name)) {
                    Some(index) => {
                        asked[index] = true;
                        args.next();
                    }
                    None => *asked = [true; Dump::ALL.len()],
                }
            }
            flag if flag.starts_with('-') => return Err(format!("unknown flag {flag}")),
            word => words.push(word),
        }
    }
    let mode = if release {
        BuildMode::Release
    } else {
        BuildMode::Debug
    };
    let given = [
        ("-o", output.is_some()),
        ("-release", release),
        ("-dump", dumps.is_some()),
    ];
    let path = |value: Option<&str>| PathBuf::from(value.unwrap_or("."));
    let check = |values: &[&str], fix: bool| -> Result<Args, String> {
        let dumps = Dump::ALL
            .into_iter()
            .zip(dumps.unwrap_or_default())
            .filter_map(|(dump, asked)| asked.then_some(dump))
            .collect();
        let path = path(value(values)?);
        Ok(Args::Check {
            path,
            dumps,
            fix,
            mode,
        })
    };
    // `-release` picks the build an LLVM IR dump is of.
    let check_takes = if dumps.is_some() {
        ["-dump", "-release"].as_slice()
    } else {
        &["-dump"]
    };
    // The command, its arguments and the flags it takes.
    let (command, args, takes) = match words.split_first() {
        Some((&command @ ("check" | "fix"), values)) => {
            (command, check(values, command == "fix")?, check_takes)
        }
        Some((&command @ "build", values)) => {
            let path = path(value(values)?);
            let args = Args::Build { path, output, mode };
            (command, args, ["-o", "-release"].as_slice())
        }
        Some((&command @ "run", values)) => {
            let (path, arguments) = values.split_first().unwrap_or((&".", &[]));
            let args = Args::Run {
                path: PathBuf::from(path),
                arguments: arguments
                    .iter()
                    .map(|argument| argument.to_string())
                    .collect(),
                mode,
            };
            (command, args, ["-release"].as_slice())
        }
        Some((&command @ "init", values)) => {
            let name = value(values)?.map(str::to_string);
            (command, Args::Init { name }, [].as_slice())
        }
        Some((&command @ "lsp", values)) => match values.first() {
            Some(extra) => return Err(format!("unexpected `{extra}`")),
            None => (command, Args::Lsp, [].as_slice()),
        },
        // `-dump` alone means `check`: every word is its own.
        _ if dumps.is_some() => ("check", check(&words, false)?, check_takes),
        Some((word, _)) => return Err(format!("unknown command `{word}`")),
        None => return Err("no command given".to_string()),
    };
    for (flag, given) in given {
        if given && !takes.contains(&flag) {
            return Err(format!("`{command}` does not take {flag}"));
        }
    }
    Ok(args)
}

/// A command's value, given in `values`: one word at most, a path or
/// `init`'s name.
fn value<'a>(values: &[&'a str]) -> Result<Option<&'a str>, String> {
    match *values {
        [] => Ok(None),
        [value] => Ok(Some(value)),
        [_, extra, ..] => Err(format!("unexpected `{extra}`")),
    }
}

#[cfg(test)]
mod tests {
    use llvm::BuildMode;

    use super::Args;
    use super::parse;

    /// The path and dump extensions `args` ask for, with "fix" added for
    /// `peeper fix`, `("lsp", [])` for `peeper lsp`, `("init", [name])` for
    /// `peeper init`, or `None`. `build` and
    /// `run` give "build" or "run", "release" for -release, then -o's value
    /// or the arguments.
    fn parsed(args: &[&str]) -> Option<(String, Vec<String>)> {
        let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
        match parse(&args).ok()? {
            Args::Check {
                path,
                dumps,
                fix,
                mode,
            } => {
                let mut asked: Vec<String> = dumps
                    .iter()
                    .map(|dump| dump.extension().to_string())
                    .collect();
                asked.extend((mode == BuildMode::Release).then(|| "release".to_string()));
                if fix {
                    asked.push("fix".to_string());
                }
                Some((path.display().to_string(), asked))
            }
            Args::Build { path, output, mode } => {
                let mut asked = vec!["build".to_string()];
                asked.extend((mode == BuildMode::Release).then(|| "release".to_string()));
                asked.extend(output.map(|output| output.display().to_string()));
                Some((path.display().to_string(), asked))
            }
            Args::Run {
                path,
                arguments,
                mode,
            } => {
                let release = (mode == BuildMode::Release).then(|| "release".to_string());
                let asked = ["run".to_string()]
                    .into_iter()
                    .chain(release)
                    .chain(arguments)
                    .collect();
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
        assert_eq!(
            parsed(&["-dump", "-release"]),
            some(".", &["thir", "mir", "ll", "release"])
        );
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
        assert_eq!(
            parsed(&["build", "-release", "-o", "out", "app"]),
            some("app", &["build", "release", "out"])
        );
        assert_eq!(parsed(&["run"]), some(".", &["run"]));
        assert_eq!(parsed(&["run", "-release"]), some(".", &["run", "release"]));
        assert_eq!(
            parsed(&["run", "app", "x", "y"]),
            some("app", &["run", "x", "y"])
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

    /// Flags come anywhere; the words left are the command and its value.
    #[test]
    fn flags_in_any_order() {
        let some = |path: &str, asked: &[&str]| {
            Some((
                path.to_string(),
                asked.iter().map(|a| a.to_string()).collect(),
            ))
        };
        let release_build = some("app", &["build", "release", "out"]);
        assert_eq!(
            parsed(&["build", "-o", "out", "app", "-release"]),
            release_build
        );
        assert_eq!(
            parsed(&["-release", "build", "app", "-o", "out"]),
            release_build
        );
        assert_eq!(
            parsed(&["build", "app", "-release", "-o", "out"]),
            release_build
        );
        assert_eq!(parsed(&["run", "-release"]), some(".", &["run", "release"]));
        assert_eq!(
            parsed(&["run", "app", "-release", "a", "b"]),
            some("app", &["run", "release", "a", "b"])
        );
        assert_eq!(
            parsed(&["-dump", "ll", "-release", "app"]),
            some("app", &["ll", "release"])
        );
        assert_eq!(
            parsed(&["check", "app", "-dump", "mir"]),
            some("app", &["mir"])
        );
        assert_eq!(
            parsed(&["-dump", "ll", "-dump", "thir"]),
            some(".", &["thir", "ll"])
        );
        assert_eq!(
            parsed(&["fix", "-dump", "mir", "app"]),
            some("app", &["mir", "fix"])
        );
    }

    /// Each mistake is named.
    #[test]
    fn errors() {
        let error = |args: &[&str]| {
            let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
            parse(&args).err()
        };
        for (args, wanted) in [
            (&[][..], "no command given"),
            (&["app"], "unknown command `app`"),
            (&["build", "-x"], "unknown flag -x"),
            (&["run", "app", "-x"], "unknown flag -x"),
            (
                &["build", "app", "-o"],
                "-o needs the program's path after it",
            ),
            (
                &["build", "-o", "-release"],
                "-o needs the program's path after it",
            ),
            (&["build", "-o", "a", "-o", "b"], "-o is given twice"),
            (&["run", "-release", "-release"], "-release is given twice"),
            (&["check", "-o", "out"], "`check` does not take -o"),
            (&["check", "-release"], "`check` does not take -release"),
            (&["run", "-dump"], "`run` does not take -dump"),
            (&["build", "a", "b"], "unexpected `b`"),
            // `ll` does not follow `-dump`, so it is the path.
            (&["-dump", "-release", "ll", "app"], "unexpected `app`"),
            (&["lsp", "app"], "unexpected `app`"),
        ] {
            assert_eq!(error(args).as_deref(), Some(wanted), "{args:?}");
        }
    }
}
