//! The Peeper compiler command.

mod args;
mod driver;
mod dump;

use std::io::IsTerminal;
use std::process::ExitCode;

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let Some(args) = args::parse(&args) else {
        eprintln!("{}", args::USAGE);
        return ExitCode::from(2);
    };
    let stderr = std::io::stderr();
    let colored = stderr.is_terminal();
    let code = driver::run(
        &args.path,
        &driver::libraries_dir(),
        &mut stderr.lock(),
        colored,
        &args.dumps,
    );
    ExitCode::from(code)
}
