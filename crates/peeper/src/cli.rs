//! The command line: what the arguments ask `peeper` to do.

use std::io::IsTerminal;
use std::io::StderrLock;
use std::process::ExitCode;

use crate::args;
use crate::args::Args;
use crate::driver;
use crate::driver::Command;

/// The command the arguments name, printing to stderr. `Err` is the exit
/// code of a command already done: arguments that make no sense, after the
/// usage is printed, or `lsp`, after serving.
pub fn command() -> Result<Command<StderrLock<'static>>, ExitCode> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let Some(args) = args::parse(&args) else {
        eprintln!("{}", args::USAGE);
        return Err(ExitCode::from(2));
    };
    let installation = driver::installation_dir();
    let stderr = std::io::stderr();
    let colored = stderr.is_terminal();
    let (path, goal) = match args {
        Args::Check { path, dumps, fix } => (
            path,
            driver::Goal::Check {
                dumps,
                apply_fixes: fix,
            },
        ),
        Args::Build { path, output } => (path, driver::Goal::Build { output }),
        Args::Run { path, arguments } => (path, driver::Goal::Run { arguments }),
        Args::Lsp => {
            return Err(match lsp::serve(&installation.join("libs")) {
                Ok(()) => ExitCode::SUCCESS,
                Err(error) => {
                    eprintln!("peeper lsp: {error}");
                    ExitCode::FAILURE
                }
            });
        }
    };
    Ok(Command {
        path,
        installation,
        out: stderr.lock(),
        colored,
        goal,
    })
}
