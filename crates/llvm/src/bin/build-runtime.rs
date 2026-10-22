//! Builds the runtime library into the folder given, as runtime/runtime.toml
//! says; scripts/build.sh runs it.
//!
//! cargo run -p llvm --bin build-runtime -- <folder>

use std::path::Path;
use std::process::ExitCode;

fn main() -> ExitCode {
    let Some(folder) = std::env::args_os().nth(1) else {
        eprintln!("usage: build-runtime <folder>");
        return ExitCode::from(2);
    };
    match llvm::runtime::build(Path::new(&folder)) {
        Ok(_) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("build-runtime: {error}");
            ExitCode::FAILURE
        }
    }
}
