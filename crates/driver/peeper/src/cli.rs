//! The command line: what the arguments ask `peeper` to do.

use std::io::IsTerminal;
use std::io::Write;
use std::process::ExitCode;

use crate::args;
use crate::args::Args;
use crate::driver;
use crate::driver::Command;

/// Does what the arguments ask, printing to stderr, and gives the exit code.
/// Arguments that make no sense print what is wrong and the usage.
pub fn run() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let args = match args::parse(&args) {
        Ok(args) => args,
        Err(error) => {
            eprintln!("peeper: {error}\n\n{}", args::USAGE);
            return ExitCode::from(2);
        }
    };
    let installation = match driver::installation_dir() {
        Ok(installation) => installation,
        Err(error) => {
            eprintln!("peeper: {error}");
            return ExitCode::FAILURE;
        }
    };
    let stderr = std::io::stderr();
    let colored = stderr.is_terminal();
    let (path, goal, target) = match args {
        Args::Program { path, goal, target } => (path, goal, target),
        Args::Init { name } => {
            let folder = match std::env::current_dir() {
                Ok(folder) => folder,
                Err(error) => {
                    eprintln!("peeper: cannot read the current folder: {error}");
                    return ExitCode::FAILURE;
                }
            };
            let name = name.unwrap_or_else(|| {
                let default = folder.file_name().unwrap_or_default().to_string_lossy();
                print!("Project name ({default}): ");
                _ = std::io::stdout().flush();
                let mut answer = String::new();
                _ = std::io::stdin().read_line(&mut answer);
                match answer.trim() {
                    "" => default.into_owned(),
                    answer => answer.to_string(),
                }
            });
            // A package name holds no space or `-`: they become `_`.
            let name = name.to_lowercase().replace([' ', '-'], "_");
            return match project::create(
                &folder,
                &name,
                &installation.join(project::LIBRARIES_FOLDER),
            ) {
                Ok(wrote_main) => {
                    if wrote_main {
                        println!("Created src/main.peep");
                    }
                    println!("Initialized project: {name}\nNext: peeper run");
                    ExitCode::SUCCESS
                }
                Err(message) => {
                    eprintln!("{message}");
                    ExitCode::FAILURE
                }
            };
        }
        Args::Lsp => {
            return match lsp::serve(&installation.join(project::LIBRARIES_FOLDER)) {
                Ok(()) => ExitCode::SUCCESS,
                Err(error) => {
                    eprintln!("peeper lsp: {error}");
                    ExitCode::FAILURE
                }
            };
        }
        Args::Help => {
            println!("{}", args::USAGE);
            return ExitCode::SUCCESS;
        }
    };
    let mut command = Command {
        path,
        target,
        installation,
        out: stderr.lock(),
        colored,
    };
    ExitCode::from(command.execute(&goal))
}
