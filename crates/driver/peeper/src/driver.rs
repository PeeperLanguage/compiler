//! What a `peeper` command does: load the program, check and compile it,
//! apply the fixes asked for, print its diagnostics, write the dumps asked
//! for, and build or run it.

use std::collections::HashMap;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use diagnostics::Diagnostic;
use diagnostics::fix;
use diagnostics::printer;
use llvm::BuildMode;
use model::DefId;
use model::machine::Machine;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use source::SourceFile;
use source::Span;

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
    /// when it ran. A folder must hold peeper.toml; its project starts at
    /// src/main.peep. A file is checked on its own, with no project and no
    /// imports.
    pub fn execute(&mut self, goal: &Goal) -> u8 {
        // Find the project: a folder's peeper.toml, or a single file.
        let project = match project::find(&self.path, &self.installation.join("libs")) {
            Ok(project) => project,
            Err(error) => {
                _ = writeln!(self.out, "{error}");
                return 1;
            }
        };
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
            Goal::Run { arguments, mode } => self.execute_run(&project, &program, arguments, *mode),
        }
    }

    /// `check`: reports the program's errors and writes the `dumps`, the
    /// LLVM IR one of a build in `mode`. With `apply_fixes`, it writes the
    /// suggested fixes first, then checks again to report only what is left.
    fn execute_check(
        &mut self,
        project: &project::Project,
        program: &project::Program,
        dumps: &[Dump],
        apply_fixes: bool,
        mode: BuildMode,
    ) -> u8 {
        let (diagnostics, ll, dumped) = compile(project, program, dumps, mode, self.target, false);
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
        program: &project::Program,
        output: Option<&Path>,
        mode: BuildMode,
    ) -> u8 {
        let (diagnostics, ll, _) = compile(project, program, &[], mode, self.target, true);
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
        project: &project::Project,
        program: &project::Program,
        arguments: &[String],
        mode: BuildMode,
    ) -> u8 {
        let (diagnostics, ll, _) = compile(project, program, &[], mode, self.target, true);
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

    /// Prints `diagnostics`; on a terminal, the source they show is colored
    /// by its tokens. Gives whether the command can go on: they were printed
    /// and none is an error.
    fn print(&mut self, diagnostics: &[Diagnostic]) -> bool {
        let scan = |file: &Arc<SourceFile>| lexer::scan(file).0;
        let highlight: Option<printer::TokensOf> = if self.colored { Some(&scan) } else { None };
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

/// Checks and lowers `program`, of `project`, for `target` unless loading
/// it found an error, gathering the `dumps` of its functions. With `links`,
/// it also needs the entry file's `main`. Gives every diagnostic, loading's
/// first; each module's LLVM text for a build in `mode`, when linking or a
/// dump needs it; and the dumps, to write when there is no error.
fn compile<'p>(
    project: &project::Project,
    program: &'p project::Program,
    dumps: &'p [Dump],
    mode: BuildMode,
    target: &'static Machine,
    links: bool,
) -> (Vec<Diagnostic>, Vec<llvm::Module<'p>>, Dumps<'p>) {
    let mut all: Vec<Diagnostic> = program.diagnostics.clone();
    let mut dumped = Dumps::new(program, dumps);
    // A program with a parse or load error is not checked: its errors
    // would follow from those.
    if !program.checkable {
        return (all, Vec::new(), dumped);
    }
    let writes_ll = links || dumped.wants_ll();
    // The module of the entry file, which must declare `main`.
    let entry = program
        .modules
        .iter()
        .position(|module| Path::new(&module.ast.file.path) == project.entry);
    let mut main = None; // the entry module's `main`, and whether it returns an exit code
    // Check and lower each function; its MIR goes to the dumps and, when
    // LLVM text is written, to the `queue` as soon as it is built.
    let mut check_and_lower = |mut queue: Option<&mut llvm::write::FunctionQueue>| {
        compile::run(
            program,
            target.pointer_bits,
            compile::Results::default(),
            false, // names are for editors
            |output| {
                let compile::Output::Function(compile::CheckedFunction {
                    definition,
                    signature,
                    thir,
                    mir,
                    types,
                    ..
                }) = output
                else {
                    return; // constants and names are for editors
                };
                if links && Some(definition.module.index()) == entry && is_main(program, definition)
                {
                    main = Some((definition, gives_exit_code(types, signature)));
                }
                let Some(mir) = mir else {
                    return; // it has errors
                };
                dumped.add(definition, thir, &mir, types);
                if let Some(queue) = queue.as_deref_mut() {
                    queue.add(definition, mir, types);
                }
            },
        )
    };
    let (mut ll, results) = if writes_ll {
        llvm::write::write_modules(program, target, mode, |queue| check_and_lower(Some(queue)))
    } else {
        (Vec::new(), check_and_lower(None))
    };
    all.extend(results.diagnostics().cloned());
    // A program that links needs a `main` that can start it.
    if links
        && !all.iter().any(Diagnostic::is_error)
        && let Some(entry) = entry
    {
        let file = &program.modules[entry].ast.file;
        match main {
            Some((main, Some(exit_code))) => ll[entry].add_entry_point(main, exit_code),
            Some((main, None)) => {
                let (name, _) = project::declared_name(&program.modules, main);
                all.push(no_entry_point(file, name.span));
            }
            None => all.push(no_entry_point(file, Span::at(0))),
        }
    }
    (all, ll, dumped)
}

/// Whether the function `definition` is named `main`.
fn is_main(program: &project::Program, definition: DefId) -> bool {
    let (name, file) = project::declared_name(&program.modules, definition);
    name.text(file) == "main"
}

/// Whether a `main` of type `ty` gives the program's exit code (`fn main() ->
/// i32`), gives none (`fn main()`), or cannot start a program (`None`).
fn gives_exit_code(types: &Types, ty: TypeId) -> Option<bool> {
    match &types[ty] {
        Type::Function {
            parameters, result, ..
        } if parameters.is_empty() => match types.real(*result) {
            TypeId::VOID => Some(false),
            TypeId::I32 => Some(true),
            _ => None,
        },
        _ => None,
    }
}

/// The error that `file`, the entry module, has no `main` a program can
/// start at; `span` is the `main` there is, or the file's start.
fn no_entry_point(file: &Arc<SourceFile>, span: Span) -> Diagnostic {
    Diagnostic::new(
        diagnostics::INVALID_ENTRY_POINT,
        "program entrypoint must be a local body-backed `fn main()` or `fn main() -> i32`",
    )
    .label(file, span, "")
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
pub fn installation_dir() -> PathBuf {
    let Ok(exe) = std::env::current_exe() else {
        return PathBuf::new();
    };
    let exe = std::fs::canonicalize(&exe).unwrap_or(exe);
    exe.parent()
        .and_then(Path::parent)
        .unwrap_or(Path::new(""))
        .to_path_buf()
}

#[cfg(test)]
mod tests;
