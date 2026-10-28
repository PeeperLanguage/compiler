//! The command line: what `peeper` is asked to do, and the usage text shown
//! when the arguments make no sense.

use std::path::PathBuf;

use llvm::BuildMode;
use llvm::Target;

use crate::driver::Goal;
use crate::dump::Dump;

pub const USAGE: &str = "\
usage: peeper check [-target name] [project folder | file.peep]
       peeper fix [-target name] [project folder | file.peep]
       peeper build [-release] [-target name] [-o program] [project folder | file.peep]
       peeper run [-release] [-target name] [project folder | file.peep] [arguments...]
       peeper [check | fix] -dump [thir | mir | ll] [-release] [-target name]
              [project folder | file.peep]
       peeper init [name]
       peeper lsp
  check  report the program's errors
  fix    apply the fixes the errors suggest, then report what is left
  build  check the program and link it into an executable, named after the
         project or file unless -o names it; -release builds it optimized,
         without debug info
  run    build the program to a temporary file and run it with the arguments
  -target  check and build the program for another machine than this one,
         such as i686-linux, 32-bit x86 Linux, which a 64-bit x86 Linux runs
         with its 32-bit C library; an unknown name lists them all
  -dump  also write each module's THIR, MIR and LLVM IR, or only the one named, to
         build/debug/dumps beside the project or file, when there are no errors;
         the LLVM IR of a debug build unless -release
  init   make this folder a project: write peeper.toml and src/main.peep,
         asking for the name unless given
  lsp    serve editors as a language server over stdin and stdout
Flags may come in any order, each with its value right after it.";

/// What the arguments ask for.
pub enum Args {
    /// `check`, `fix`, `build` or `run`: `goal`, for the program in the
    /// project folder or file `path`, built for `target`. A build is
    /// release with -release.
    Program {
        path: PathBuf,
        goal: Goal,
        target: &'static Target,
    },
    Init {
        name: Option<String>, // asked for when not given
    },
    Lsp,
}

/// The arguments after the program's name, or what is wrong with them.
/// Flags come anywhere, each with its value right after it: `-o` and
/// `-target` must have one, `-dump` takes the next word when it names a
/// dump. The words left, in order, are the command and its value; `run`
/// gives the words after its path to the program. With no command, `-dump`
/// means `check`.
pub fn parse(args: &[String]) -> Result<Args, String> {
    let kind = |name: &str| Dump::ALL.iter().position(|kind| kind.extension() == name);
    let mut output = None;
    let mut target = None;
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
            "-target" if target.is_some() => return Err("-target is given twice".to_string()),
            "-target" => match args.next_if(|value| !value.starts_with('-')) {
                Some(name) => target = Some(Target::named(name)?),
                None => return Err("-target needs a target's name after it".to_string()),
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
        ("-target", target.is_some()),
        ("-release", release),
        ("-dump", dumps.is_some()),
    ];
    let target = match target {
        Some(target) => target,
        None => Target::host()?,
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
    // `-release` picks the build an LLVM IR dump is of.
    let check_takes = if dumps.is_some() {
        ["-dump", "-release", "-target"].as_slice()
    } else {
        &["-dump", "-target"]
    };
    // The command, its arguments and the flags it takes.
    let (command, args, takes) = match words.split_first() {
        Some((&command @ ("check" | "fix"), values)) => {
            (command, check(values, command == "fix")?, check_takes)
        }
        Some((&command @ "build", values)) => {
            let path = path(value(values)?);
            let goal = Goal::Build { output, mode };
            (
                command,
                Args::Program { path, goal, target },
                ["-o", "-release", "-target"].as_slice(),
            )
        }
        Some((&command @ "run", values)) => {
            let (path, arguments) = values.split_first().unwrap_or((&".", &[]));
            let goal = Goal::Run {
                arguments: arguments
                    .iter()
                    .map(|argument| argument.to_string())
                    .collect(),
                mode,
            };
            let path = PathBuf::from(path);
            (
                command,
                Args::Program { path, goal, target },
                ["-release", "-target"].as_slice(),
            )
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
mod tests;
