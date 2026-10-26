//! The command line: what the arguments ask `peeper` to do.

use std::io::IsTerminal;
use std::io::StderrLock;
use std::io::Write;
use std::process::ExitCode;

use crate::args;
use crate::args::Args;
use crate::driver;
use crate::driver::Command;
use crate::driver::Goal;

/// The command the arguments name, printing to stderr, and its goal. `Err`
/// is the exit code of a command already done: arguments that make no sense,
/// after what is wrong and the usage are printed, `init`, after making the project, or `lsp`,
/// after serving.
pub fn parse_command() -> Result<(Command<StderrLock<'static>>, Goal), ExitCode> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let args = match args::parse(&args) {
        Ok(args) => args,
        Err(error) => {
            eprintln!("peeper: {error}\n\n{}", args::USAGE);
            return Err(ExitCode::from(2));
        }
    };
    let installation = driver::installation_dir();
    let stderr = std::io::stderr();
    let colored = stderr.is_terminal();
    let (path, goal) = match args {
        Args::Program { path, goal } => (path, goal),
        Args::Init { name } => {
            let folder = std::env::current_dir().unwrap_or_default();
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
            return Err(
                match project::create(&folder, &name, &installation.join("libs")) {
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
                },
            );
        }
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
    let command = Command {
        path,
        installation,
        out: stderr.lock(),
        colored,
    };
    Ok((command, goal))
}
