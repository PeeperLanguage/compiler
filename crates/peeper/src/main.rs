//! The Peeper compiler command.

mod args;
mod cli;
mod driver;
mod dump;

use std::process::ExitCode;

fn main() -> ExitCode {
    let (mut command, goal) = match cli::command() {
        Ok(command) => command,
        Err(code) => return code,
    };
    ExitCode::from(command.run(&goal))
}
