//! The Peeper compiler command.

mod args;
mod driver;
mod dump;

use std::io::IsTerminal;
use std::process::ExitCode;

use args::Args;

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let Some(args) = args::parse(&args) else {
        eprintln!("{}", args::USAGE);
        return ExitCode::from(2);
    };
    match args {
        Args::Check { path, dumps, fix } => {
            let stderr = std::io::stderr();
            let colored = stderr.is_terminal();
            let code = driver::run(
                &path,
                &driver::libraries_dir(),
                &mut stderr.lock(),
                colored,
                &dumps,
                fix,
            );
            ExitCode::from(code)
        }
        Args::Lsp => match lsp::serve(&driver::libraries_dir()) {
            Ok(()) => ExitCode::SUCCESS,
            Err(error) => {
                eprintln!("peeper lsp: {error}");
                ExitCode::FAILURE
            }
        },
    }
}
