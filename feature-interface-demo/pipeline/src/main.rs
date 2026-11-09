//! `pipeline <file.peep> [--native | --mir | --ll]`: compiles the file for
//! a 64-bit target and prints what its `main` returns, run on the
//! interpreter, or with `--native` built by clang and run; with `--mir` or
//! `--ll` it prints the MIR after cleanup or the LLVM text instead. Errors
//! and warnings go to stderr, in source order; an error exits with 1.

use std::process::ExitCode;

use model::facts::Target;
use pipeline::Output;

const USAGE: &str = "usage: pipeline <file.peep> [--native | --mir | --ll]";

fn main() -> ExitCode {
    let arguments: Vec<String> = std::env::args().skip(1).collect();
    let (path, flag) = match arguments.as_slice() {
        [path] => (path, None),
        [path, flag] => (path, Some(flag.as_str())),
        _ => {
            eprintln!("{USAGE}");
            return ExitCode::from(2);
        }
    };
    let source = match std::fs::read_to_string(path) {
        Ok(source) => source,
        Err(error) => {
            eprintln!("{path}: {error}");
            return ExitCode::from(1);
        }
    };
    let target = Target { pointer_bytes: 8 };
    // shortcut: prints `main`'s bits as an unsigned number, so an `i32`
    // -1 shows as 4294967295; sign it once `main` may return other types.
    let number = |output: Output<u64>| Output {
        value: format!("{}\n", output.value),
        warnings: output.warnings,
    };
    let printed = match flag {
        None => pipeline::run(&source, target).map(number),
        Some("--native") => pipeline::run_native(&source, target).map(number),
        Some("--mir") => pipeline::mir(&source, target),
        Some("--ll") => pipeline::llvm(&source, target),
        Some(_) => {
            eprintln!("{USAGE}");
            return ExitCode::from(2);
        }
    };
    match printed {
        Ok(output) => {
            (output.warnings.iter()).for_each(|warning| eprintln!("{warning}"));
            print!("{}", output.value);
            ExitCode::SUCCESS
        }
        Err(messages) => {
            messages.iter().for_each(|message| eprintln!("{message}"));
            ExitCode::from(1)
        }
    }
}
