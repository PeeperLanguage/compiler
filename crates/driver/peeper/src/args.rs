//! The command line: what `peeper` is asked to do, and the usage text shown
//! when the arguments make no sense.

use std::path::PathBuf;

use llvm::BuildMode;
use model::machine::Machine;

use crate::driver::Goal;
use crate::dump::Dump;

pub const USAGE: &str = "\
usage: peeper check [-target name] [project folder | file.peep]
       peeper fix [-target name] [project folder | file.peep]
       peeper build [-release] [-target name] [-o program] [project folder | file.peep]
       peeper run [-release] [-target name] [project folder | file.peep] [-- arguments...]
       peeper run -interpret [-target name] [project folder | file.peep]
       peeper [check | fix] -dump [thir | mir | ll] [-release] [-target name]
              [project folder | file.peep]
       peeper init [name]
       peeper lsp
       peeper -help
  check  report the program's errors
  fix    apply the fixes the errors suggest, then report what is left
  build  check the program and link it into an executable, named after the
         project or file unless -o names it; -release builds it optimized,
         without debug info
  run    build the program to a temporary file and run it with the arguments
         after --
  -interpret  experimental: run the program on the MIR interpreter, with no
         LLVM, linker or toolchain. Its output comes when it ends, its input is
         empty, and it stops at the interpreter's limits; no -release or --
  -target  check and build the program for another machine than this one,
         such as i686-linux, 32-bit x86 Linux, which a 64-bit x86 Linux runs
         with its 32-bit C library; an unknown name lists them all
  -dump  also write each module's THIR, MIR and LLVM IR, or only the one named, to
         build/debug/dumps beside the project or file, when there are no errors;
         the LLVM IR of a debug build unless -release
  init   make this folder a project: write peeper.toml and src/main.peep,
         asking for the name unless given
  lsp    serve editors as a language server over stdin and stdout
  -help  print this usage text
Flags may come in any order before --, each with its value right after it.";

const CHECK: u16 = 1 << 0;
const FIX: u16 = 1 << 1;
const BUILD: u16 = 1 << 2;
const RUN: u16 = 1 << 3;
const DUMP: u16 = 1 << 4;
const INIT: u16 = 1 << 5;
const LSP: u16 = 1 << 6;
const HELP: u16 = 1 << 7;
const INTERPRET: u16 = 1 << 8;

/// What the arguments ask for.
pub enum Args {
    /// `check`, `fix`, `build` or `run`: `goal`, for the program in the
    /// project folder or file `path`, built for `target`. A build is
    /// release with -release.
    Program {
        path: PathBuf,
        goal: Goal,
        target: &'static Machine,
    },
    Init {
        name: Option<String>, // asked for when not given
    },
    Lsp,
    Help,
}

/// The arguments after the program's name, or what is wrong with them.
/// Flags come anywhere before `--`, each with its value right after it: `-o`
/// and `-target` must have one, `-dump` takes the next word when it names a
/// dump. The words left, in order, are the command and its value; `run`
/// gives the words after `--` to the program. With no command, `-dump`
/// means `check`.
pub fn parse(args: &[String]) -> Result<Args, String> {
    let (compiler_args, program_args) = match args.iter().position(|arg| arg == "--") {
        Some(at) => (&args[..at], Some(&args[at + 1..])),
        None => (args, None),
    };
    let kind = |name: &str| Dump::ALL.iter().position(|kind| kind.extension() == name);
    let mut output = None;
    let mut target = None;
    let mut release = false;
    let mut interpret = false;
    let mut help = false;
    let mut dumps: Option<[bool; Dump::ALL.len()]> = None; // which were asked for, by -dump
    let mut words = Vec::new();
    let mut args = compiler_args.iter().map(String::as_str).peekable();
    while let Some(arg) = args.next() {
        match arg {
            "-o" if output.is_some() => return Err("-o is given twice".to_string()),
            "-o" => match args.next_if(|value| !value.starts_with('-')) {
                Some(value) => output = Some(PathBuf::from(value)),
                None => return Err("-o needs the program's path after it".to_string()),
            },
            "-target" if target.is_some() => return Err("-target is given twice".to_string()),
            "-target" => match args.next_if(|value| !value.starts_with('-')) {
                Some(name) => target = Some(Machine::named(name)?),
                None => return Err("-target needs a target's name after it".to_string()),
            },
            "-release" if release => return Err("-release is given twice".to_string()),
            "-release" => release = true,
            "-interpret" if interpret => return Err("-interpret is given twice".to_string()),
            "-interpret" => interpret = true,
            "-help" if help => return Err("-help is given twice".to_string()),
            "-help" => help = true,
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
        ("-o", output.is_some(), BUILD),
        (
            "-target",
            target.is_some(),
            CHECK | FIX | BUILD | RUN | DUMP | INTERPRET,
        ),
        ("-release", release, BUILD | RUN | DUMP),
        ("-interpret", interpret, INTERPRET),
        ("-dump", dumps.is_some(), CHECK | FIX | DUMP),
        ("-help", help, HELP),
        ("--", program_args.is_some(), RUN),
    ];
    let target = match target {
        Some(target) => target,
        None => Machine::host()?,
    };
    let path = |value: Option<&str>| PathBuf::from(value.unwrap_or("."));
    let check = |values: &[&str], apply_fixes: bool| -> Result<Args, String> {
        let dumps = Dump::ALL
            .into_iter()
            .zip(dumps.unwrap_or_default())
            .filter_map(|(dump, asked)| asked.then_some(dump))
            .collect();
        let path = path(value(values)?);
        let goal = Goal::Check {
            dumps,
            apply_fixes,
            mode,
        };
        Ok(Args::Program { path, goal, target })
    };
    // `-dump` lets `check` and `fix` take `-release` for the LLVM IR dump.
    let dump_bit = if dumps.is_some() { DUMP } else { 0 };
    // The command, its arguments and the bitmask of flags it takes.
    let (command, args, mask) = match words.split_first() {
        Some((&command @ ("check" | "fix"), values)) => {
            let bit = if command == "fix" { FIX } else { CHECK };
            (command, check(values, command == "fix")?, bit | dump_bit)
        }
        Some((&command @ "build", values)) => {
            let path = path(value(values)?);
            let goal = Goal::Build { output, mode };
            (command, Args::Program { path, goal, target }, BUILD)
        }
        Some((&"run", values)) if interpret => {
            let path = path(value(values)?);
            let goal = Goal::Interpret;
            let args = Args::Program { path, goal, target };
            ("run -interpret", args, INTERPRET)
        }
        Some((&command @ "run", values)) => {
            let path = path(value(values)?);
            let goal = Goal::Run {
                arguments: program_args.unwrap_or_default().to_vec(),
                mode,
            };
            (command, Args::Program { path, goal, target }, RUN)
        }
        Some((&command @ "init", values)) => {
            let name = value(values)?.map(str::to_string);
            (command, Args::Init { name }, INIT)
        }
        Some((&command @ "lsp", values)) => match values.first() {
            Some(extra) => return Err(format!("unexpected `{extra}`")),
            None => (command, Args::Lsp, LSP),
        },
        // `-dump` alone means `check`: every word is its own.
        _ if dumps.is_some() => ("check", check(&words, false)?, DUMP),
        None if help => ("-help", Args::Help, HELP),
        Some((word, _)) if help => return Err(format!("unexpected `{word}`")),
        Some((word, _)) => return Err(format!("unknown command `{word}`")),
        None => return Err("no command given".to_string()),
    };
    for (flag, given, allowed) in given {
        if given && mask & allowed == 0 {
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
    use super::Args;
    use super::parse;
    use llvm::BuildMode;
    use model::machine::Machine;

    use crate::driver::Goal;

    /// The path and dump extensions `args` ask for, with "fix" added for
    /// `peeper fix`, `("lsp", [])` for `peeper lsp`, `("init", [name])` for
    /// `peeper init`, or `None`. `build` and
    /// `run` give "build" or "run", "release" for -release, then -o's value
    /// or the arguments.
    fn parsed(args: &[&str]) -> Option<(String, Vec<String>)> {
        let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
        let (path, goal) = match parse(&args).ok()? {
            Args::Program { path, goal, .. } => (path, goal),
            Args::Lsp => return Some(("lsp".to_string(), Vec::new())),
            Args::Help => return Some(("help".to_string(), Vec::new())),
            Args::Init { name } => return Some(("init".to_string(), name.into_iter().collect())),
        };
        match goal {
            Goal::Check {
                dumps,
                apply_fixes,
                mode,
            } => {
                let mut asked: Vec<String> = dumps
                    .iter()
                    .map(|dump| dump.extension().to_string())
                    .collect();
                asked.extend((mode == BuildMode::Release).then(|| "release".to_string()));
                if apply_fixes {
                    asked.push("fix".to_string());
                }
                Some((path.display().to_string(), asked))
            }
            Goal::Build { output, mode } => {
                let mut asked = vec!["build".to_string()];
                asked.extend((mode == BuildMode::Release).then(|| "release".to_string()));
                asked.extend(output.map(|output| output.display().to_string()));
                Some((path.display().to_string(), asked))
            }
            Goal::Run { arguments, mode } => {
                let release = (mode == BuildMode::Release).then(|| "release".to_string());
                let asked = ["run".to_string()]
                    .into_iter()
                    .chain(release)
                    .chain(arguments)
                    .collect();
                Some((path.display().to_string(), asked))
            }
            Goal::Interpret => Some((path.display().to_string(), vec!["interpret".to_string()])),
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
        assert_eq!(parsed(&["run", "--", "x"]), some(".", &["run", "x"]));
        assert_eq!(parsed(&["run", "-release"]), some(".", &["run", "release"]));
        assert_eq!(
            parsed(&["run", "app", "--", "x", "-help"]),
            some("app", &["run", "x", "-help"])
        );
        assert_eq!(parsed(&["-help"]), some("help", &[]));
        assert_eq!(parsed(&["lsp"]), some("lsp", &[]));
        assert_eq!(parsed(&["init"]), some("init", &[]));
        assert_eq!(parsed(&["init", "app"]), some("init", &["app"]));
        assert_eq!(parsed(&["check", "-target", "i686-linux"]), some(".", &[]));
        assert_eq!(
            parsed(&["-dump", "ll", "-target", "i686-linux", "app"]),
            some("app", &["ll"])
        );
        assert_eq!(
            parsed(&["build", "-target", "i686-linux", "-o", "out", "app"]),
            some("app", &["build", "out"])
        );
        assert_eq!(
            parsed(&["run", "-target", "i686-linux", "-release", "app", "--", "x"]),
            some("app", &["run", "release", "x"])
        );
        for wrong in [
            &[][..],
            &["build", "-o"],
            &["build", "a", "b"],
            &["run", "app", "x"],
            &["run", "app", "-help"],
            &["-help", "app"],
            &["-dump", "asm", "app"],
            &["check", "a", "b"],
            &["fix", "a", "b"],
            &["lsp", "app"],
            &["init", "a", "b"],
        ] {
            assert_eq!(parsed(wrong), None, "{wrong:?}");
        }
    }

    /// `-target` picks the target it names, and the host is the target
    /// without it.
    #[test]
    fn targets() {
        let target = |args: &[&str]| {
            let args: Vec<String> = args.iter().map(|arg| arg.to_string()).collect();
            match parse(&args) {
                Ok(Args::Program { target, .. }) => target,
                _ => panic!("{args:?} names a program"),
            }
        };
        let i686 = Machine::named("i686-linux").unwrap();
        assert_eq!(target(&["check", "-target", "i686-linux"]), i686);
        assert_eq!(
            target(&["-dump", "ll", "-target", "i686-linux", "app"]),
            i686
        );
        assert_eq!(
            target(&["build", "-target", "i686-linux", "-o", "out"]),
            i686
        );
        assert_eq!(
            target(&["run", "app", "-target", "i686-linux", "--", "x"]),
            i686
        );
        assert_eq!(Ok(target(&["run", "app"])), Machine::host());
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
            parsed(&["run", "app", "-release", "--", "a", "b"]),
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
        // It lists every target, so a new one changes it.
        let unknown = Machine::named("arm").unwrap_err();
        assert!(unknown.starts_with("unknown target `arm`; the targets are x86_64-linux, "));
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
            (
                &["run", "-target"],
                "-target needs a target's name after it",
            ),
            (&["build", "-target", "arm"], unknown.as_str()),
            (
                &["run", "-target", "i686-linux", "-target", "i686-linux"],
                "-target is given twice",
            ),
            (
                &["init", "-target", "i686-linux"],
                "`init` does not take -target",
            ),
            (&["run", "-release", "-release"], "-release is given twice"),
            (
                &["run", "-interpret", "-interpret"],
                "-interpret is given twice",
            ),
            (
                &["run", "-interpret", "-release"],
                "`run -interpret` does not take -release",
            ),
            (
                &["run", "-interpret", "--", "x"],
                "`run -interpret` does not take --",
            ),
            (
                &["run", "-interpret", "-o", "out"],
                "`run -interpret` does not take -o",
            ),
            (&["run", "-interpret", "a", "b"], "unexpected `b`"),
            (&["build", "-interpret"], "`build` does not take -interpret"),
            (&["check", "-interpret"], "`check` does not take -interpret"),
            (&["-dump", "-interpret"], "`check` does not take -interpret"),
            (&["-help", "-help"], "-help is given twice"),
            (&["check", "-o", "out"], "`check` does not take -o"),
            (&["check", "-release"], "`check` does not take -release"),
            (&["run", "-dump"], "`run` does not take -dump"),
            (&["run", "app", "-help"], "`run` does not take -help"),
            (&["-help", "-release"], "`-help` does not take -release"),
            (&["build", "app", "--", "x"], "`build` does not take --"),
            (&["build", "a", "b"], "unexpected `b`"),
            (&["run", "a", "b"], "unexpected `b`"),
            // `ll` does not follow `-dump`, so it is the path.
            (&["-dump", "-release", "ll", "app"], "unexpected `app`"),
            (&["lsp", "app"], "unexpected `app`"),
            (&["-help", "app"], "unexpected `app`"),
        ] {
            assert_eq!(error(args).as_deref(), Some(wanted), "{args:?}");
        }
    }

    #[test]
    fn run_on_the_interpreter() {
        let interpreted = |path: &str| Some((path.to_string(), vec!["interpret".to_string()]));
        assert_eq!(parsed(&["run", "-interpret"]), interpreted("."));
        assert_eq!(parsed(&["-interpret", "run", "app"]), interpreted("app"));
        assert_eq!(
            parsed(&["run", "-interpret", "-target", "i686-linux", "app"]),
            interpreted("app")
        );
    }
}
