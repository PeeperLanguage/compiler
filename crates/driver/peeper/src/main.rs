//! The Peeper compiler command.

mod args;
mod cli;
mod driver;
mod dump;

use std::process::ExitCode;

fn main() -> ExitCode {
    cli::run()
}
