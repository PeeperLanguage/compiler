//! What a `peeper` command does: load the program, check and compile it,
//! apply the fixes asked for, print its diagnostics, write the dumps asked
//! for, and build or run it.

use std::collections::HashMap;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;

use diagnostics::Diagnostic;
use diagnostics::fix;
use diagnostics::printer;
use eval::interpreter::program::Stream;
use llvm::BuildMode;
use model::machine::Machine;

use crate::dump::Dump;
use crate::dump::Dumps;

/// What a command does with the program it loads.
pub enum Goal {
    /// Write the `dumps`, the LLVM IR one of a build in `mode`; with
    /// `apply_fixes`, write the fixes the diagnostics suggest to the
    /// project's files, list them, and check again to report what is left.
    Check {
        dumps: Vec<Dump>,
        apply_fixes: bool,
        mode: BuildMode,
    },
    /// Link the program, built in `mode`, into `output`: by default the
    /// program's name, in the current folder.
    Build {
        output: Option<PathBuf>,
        mode: BuildMode,
    },
    /// Link it, built in `mode`, to a temporary file and run it with
    /// `arguments`.
    Run {
        arguments: Vec<String>,
        mode: BuildMode,
    },
    /// Run it on the interpreter, with no backend or linker.
    Interpret,
}

/// Where a `peeper` command runs: the program it is given, the machine it
/// is built for, the installation it uses and where it prints. What it does
/// there is a `Goal`.
pub struct Command<W: Write> {
    pub path: PathBuf,            // the project folder or file
    pub target: &'static Machine, // what it is checked and built for (`-target`)
    pub installation: PathBuf, // the bundled libraries, runtime and toolchain (`installation_dir`)
    pub out: W,                // where diagnostics and messages are printed
    pub colored: bool,         // whether diagnostics are printed in color
}

impl<W: Write> Command<W> {
    /// Loads the program at `path` and does what `goal` asks. The result is
    /// the exit code: 1 when there are errors, else 0, or the program's own
    /// when it ran. A folder must hold peeper.toml; its program starts at
    /// src/main.peep, and a library, which only `check` takes, is each file
    /// of src. A file is checked on its own, with no project and no imports.
    pub fn execute(&mut self, goal: &Goal) -> u8 {
        // Find the project: a folder's peeper.toml, or a single file.
        let project = match project::find(
            &self.path,
            &self.installation.join(project::LIBRARIES_FOLDER),
        ) {
            Ok(project) => project,
            Err(error) => {
                _ = writeln!(self.out, "{error}");
                return 1;
            }
        };
        if project.entry.is_none() && !matches!(goal, Goal::Check { .. }) {
            let message = project::manifest::library_has_no_main(&project.name);
            _ = writeln!(self.out, "peeper: {message}");
            return 1;
        }
        // Read and parse every module, following imports. No editor has
        // unsaved text here, so every file is read from disk.
        let program = project::load(&project, &HashMap::<PathBuf, String>::new());
        match goal {
            Goal::Check {
                dumps,
                apply_fixes,
                mode,
            } => self.execute_check(&project, &program, dumps, *apply_fixes, *mode),
            Goal::Build { output, mode } => {
                self.execute_build(&project, &program, output.as_deref(), *mode)
            }
            Goal::Run { arguments, mode } => self.execute_run(&program, arguments, *mode),
            Goal::Interpret => self.execute_interpret(&program),
        }
    }

    /// `check`: reports the program's errors and writes the `dumps`, the
    /// LLVM IR one of a build in `mode`. With `apply_fixes`, it writes the
    /// suggested fixes first, then checks again to report only what is left.
    fn execute_check(
        &mut self,
        project: &project::Project,
        program: &model::program::Program,
        dumps: &[Dump],
        apply_fixes: bool,
        mode: BuildMode,
    ) -> u8 {
        let (diagnostics, ll, dumped) = compile(program, dumps, mode, self.target, false);
        // Dumps are written only for a program without errors.
        if !diagnostics.iter().any(Diagnostic::is_error)
            && let Err(error) =
                dumped.write(&project.root.join("build/debug/dumps"), &ll, &mut self.out)
        {
            _ = writeln!(self.out, "peeper: cannot write a dump: {error}");
            return 1;
        }
        if apply_fixes {
            let fixes = fix::chosen(&diagnostics, &project.root);
            if !fixes.is_empty() {
                if let Err(error) = fix::write(&fixes, &mut self.out) {
                    _ = writeln!(self.out, "peeper: cannot write a fix: {error}");
                    return 1;
                }
                let again = Goal::Check {
                    dumps: dumps.to_vec(),
                    apply_fixes: false,
                    mode,
                };
                return self.execute(&again);
            }
        }
        u8::from(!self.print(&diagnostics))
    }

    /// `build`: links the program, built in `mode`, into `output`, by default
    /// the program's name in the current folder.
    fn execute_build(
        &mut self,
        project: &project::Project,
        program: &model::program::Program,
        output: Option<&Path>,
        mode: BuildMode,
    ) -> u8 {
        let (diagnostics, ll, _) = compile(program, &[], mode, self.target, true);
        if !self.print(&diagnostics) {
            return 1;
        }
        let output = output.map_or_else(
            || {
                PathBuf::from(format!(
                    "{}{}",
                    project.name,
                    self.target.executable_suffix()
                ))
            },
            Path::to_path_buf,
        );
        if !self.link(&ll, mode, &output) {
            return 1;
        }
        _ = writeln!(self.out, "Built {}", output.display());
        0
    }

    /// `run`: links the program, built in `mode`, to a temporary file, runs
    /// it with `arguments` and removes it; gives the program's exit code.
    fn execute_run(
        &mut self,
        program: &model::program::Program,
        arguments: &[String],
        mode: BuildMode,
    ) -> u8 {
        let (diagnostics, ll, _) = compile(program, &[], mode, self.target, true);
        if !self.print(&diagnostics) {
            return 1;
        }
        let output = std::env::temp_dir().join(format!(
            "peeper-run-{}{}",
            std::process::id(),
            self.target.executable_suffix()
        ));
        if !self.link(&ll, mode, &output) {
            return 1;
        }
        _ = self.out.flush();
        let status = std::process::Command::new(&output).args(arguments).status();
        _ = std::fs::remove_file(&output);
        match status {
            Ok(status) => exit_code_of(status),
            Err(error) => {
                _ = writeln!(self.out, "peeper: cannot run the program: {error}");
                1
            }
        }
    }

    /// `run -interpret`: runs the program on the interpreter, then prints what
    /// it wrote; gives the program's exit code.
    fn execute_interpret(&mut self, program: &model::program::Program) -> u8 {
        let (results, ran) = compile::interpret(program, self.target);
        let found = program.diagnostics.iter().chain(results.diagnostics());
        let diagnostics: Vec<Diagnostic> = found.cloned().collect();
        if !self.print(&diagnostics) {
            return 1;
        }
        let ran = ran.expect("a program with no errors starts");
        // shortcut: output comes only when the run ends, as `Outcome` holds it;
        // the interpreter should write as it goes if this mode stays.
        let mut stdout = std::io::stdout();
        for written in &ran.output {
            match written.stream {
                // At once, as both streams may be one terminal.
                Stream::Stdout => _ = stdout.write_all(&written.text).and(stdout.flush()),
                Stream::Stderr => _ = self.out.write_all(&written.text),
            }
        }
        ran.exit_code
    }

    /// Prints `diagnostics`; on a terminal, the source they show is colored
    /// by its tokens. Gives whether the command can go on: they were printed
    /// and none is an error.
    fn print(&mut self, diagnostics: &[Diagnostic]) -> bool {
        let highlight: Option<printer::HighlightsOf> = if self.colored {
            Some(&lexer::highlights)
        } else {
            None
        };
        if let Err(error) = printer::print(&mut self.out, diagnostics, highlight) {
            eprintln!("peeper: cannot write diagnostics: {error}");
            return false;
        }
        !diagnostics.iter().any(Diagnostic::is_error)
    }

    /// Links the modules' LLVM text `ll`, built in `mode`, with the runtime
    /// of the command's target into the program `output`; gives whether it
    /// did, a failure reported.
    fn link(&mut self, ll: &[llvm::Module], mode: BuildMode, output: &Path) -> bool {
        if let Err(error) =
            llvm::link::link_executable(ll, mode, self.target, &self.installation, output)
        {
            _ = writeln!(self.out, "peeper: {error}");
            return false;
        }
        true
    }
}

/// Checks and lowers `program` for `target`, gathering the `dumps` of its
/// functions. With `links`, it also needs the entry file's `main`. Gives
/// every diagnostic, loading's first; each module's LLVM text for a build in
/// `mode`, when linking or a dump needs it; and the dumps, to write when
/// there is no error.
fn compile<'p>(
    program: &'p model::program::Program,
    dumps: &'p [Dump],
    mode: BuildMode,
    target: &'static Machine,
    links: bool,
) -> (Vec<Diagnostic>, Vec<llvm::Module<'p>>, Dumps<'p>) {
    let mut dumped = Dumps::new(program, dumps);
    let (ll, results) = if links || dumped.wants_ll() {
        let dump = |function, thir: &_, mir: &_, types: &_| dumped.add(function, thir, mir, types);
        peeper::write_program(program, target, mode, links, dump)
    } else {
        let results = compile::run(
            program,
            target,
            compile::Results::default(),
            false,
            false, // names are for editors
            |output| {
                // `peeper check` reads no MIR, so it does not pay for it.
                let compile::Output::Function(checked) = output else {
                    return;
                };
                if dumps.is_empty() {
                    return;
                }
                let compile::CheckedFunction {
                    function,
                    thir,
                    types,
                    ..
                } = checked;
                if let Some(mir) = checked.into_mir() {
                    dumped.add(function, thir, &mir, types);
                }
            },
        );
        (Vec::new(), results)
    };
    let found = program.diagnostics.iter().chain(results.diagnostics());
    (found.cloned().collect(), ll, dumped)
}

/// The exit code a program ended with; one a signal stopped is 128 plus the
/// signal's number, as shells report it.
fn exit_code_of(status: std::process::ExitStatus) -> u8 {
    #[cfg(unix)]
    if let Some(signal) = std::os::unix::process::ExitStatusExt::signal(&status) {
        return (128 + signal) as u8;
    }
    status.code().map_or(1, |code| code as u8)
}

/// The installation peeper runs from: the executable is in <root>/bin, the
/// libraries in <root>/libs, the runtime in <root>/runtime and the LLVM
/// toolchain in <root>/toolchains/native.
/// The error is why it cannot be found, as a full message: with no
/// installation, no program finds its libraries.
pub fn installation_dir() -> Result<PathBuf, String> {
    let exe = std::env::current_exe()
        .map_err(|error| format!("cannot find the peeper executable: {error}"))?;
    let exe = std::fs::canonicalize(&exe).map_err(|error| format!("{}: {error}", exe.display()))?;
    match exe.parent().and_then(Path::parent) {
        Some(root) => Ok(root.to_path_buf()),
        None => Err(format!(
            "{}: not in a bin folder of an installation",
            exe.display()
        )),
    }
}

#[cfg(test)]
mod tests;
