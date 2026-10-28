//! Builds the runtime library into the installation's runtime folder given,
//! as runtime/runtime.toml says, for the host or the target named;
//! scripts/build.sh runs it.
//!
//! cargo run -p llvm --bin build-runtime -- <folder> [target]

use std::path::Path;
use std::process::ExitCode;

use model::machine::Machine;

fn main() -> ExitCode {
    let mut args = std::env::args_os().skip(1);
    let usage = "usage: build-runtime <folder> [target]";
    let Some(folder) = args.next() else {
        eprintln!("{usage}");
        return ExitCode::from(2);
    };
    let target = match args.next() {
        None => Machine::host(),
        Some(name) => Machine::named(&name.to_string_lossy()),
    };
    let target = match target {
        Ok(target) => target,
        Err(error) => {
            eprintln!("build-runtime: {error}\n{usage}");
            return ExitCode::from(2);
        }
    };
    match llvm::runtime::build(Path::new(&folder), target) {
        Ok(_) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("build-runtime: {error}");
            ExitCode::FAILURE
        }
    }
}
