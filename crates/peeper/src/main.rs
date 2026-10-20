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
    let installation = driver::installation_dir();
    let stderr = std::io::stderr();
    let colored = stderr.is_terminal();
    let (path, goal) = match &args {
        Args::Check { path, dumps, fix } => (
            path,
            driver::Goal::Check {
                dumps,
                apply_fixes: *fix,
            },
        ),
        Args::Build { path, output } => (
            path,
            driver::Goal::Build {
                output: output.as_deref(),
            },
        ),
        Args::Run { path, arguments } => (path, driver::Goal::Run { arguments }),
        Args::Lsp => {
            return match lsp::serve(&installation.join("libs")) {
                Ok(()) => ExitCode::SUCCESS,
                Err(error) => {
                    eprintln!("peeper lsp: {error}");
                    ExitCode::FAILURE
                }
            };
        }
    };
    let code = driver::run(path, &installation, &mut stderr.lock(), colored, &goal);
    ExitCode::from(code)
}
