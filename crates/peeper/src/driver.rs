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
use model::DefId;
use model::ModuleId;
use model::semantic_types::Type;
use model::semantic_types::TypeId;
use source::SourceFile;
use source::Span;

use crate::dump::Dump;
use crate::dump::Dumps;

/// What a command does with the program it loads.
pub enum Goal {
    /// Write the `dumps`; with `apply_fixes`, write the fixes the diagnostics
    /// suggest to the project's files, list them, and check again to report
    /// what is left.
    Check { dumps: Vec<Dump>, apply_fixes: bool },
    /// Link the program into `output`: by default the program's name, in the
    /// current folder.
    Build { output: Option<PathBuf> },
    /// Link it to a temporary file and run it with `arguments`.
    Run { arguments: Vec<String> },
}

/// Where a `peeper` command runs: the program it is given, the installation
/// it uses and where it prints. What it does there is a `Goal`.
pub struct Command<W: Write> {
    pub path: PathBuf,         // the project folder or file
    pub installation: PathBuf, // the bundled libraries and runtime (`installation_dir`)
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
            Goal::Check { dumps, apply_fixes } => {
                self.execute_check(&project, &program, dumps, *apply_fixes)
            }
            Goal::Build { output } => self.execute_build(&project, &program, output.as_deref()),
            Goal::Run { arguments } => self.execute_run(&project, &program, arguments),
        }
    }

    /// `check`: reports the program's errors and writes the `dumps`. With
    /// `apply_fixes`, it writes the suggested fixes first, then checks again
    /// to report only what is left.
    fn execute_check(
        &mut self,
        project: &project::Project,
        program: &project::Program,
        dumps: &[Dump],
        apply_fixes: bool,
    ) -> u8 {
        let (diagnostics, ll, dumped) = compile(project, program, dumps, false);
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
                };
                return self.execute(&again);
            }
        }
        u8::from(!self.print(&diagnostics))
    }

    /// `build`: links the program into `output`, by default the program's
    /// name in the current folder.
    fn execute_build(
        &mut self,
        project: &project::Project,
        program: &project::Program,
        output: Option<&Path>,
    ) -> u8 {
        let (diagnostics, ll, _) = compile(project, program, &[], true);
        if !self.print(&diagnostics) {
            return 1;
        }
        let output = output.map_or_else(
            || PathBuf::from(format!("{}{}", project.name, std::env::consts::EXE_SUFFIX)),
            Path::to_path_buf,
        );
        if !self.link(&ll, &output) {
            return 1;
        }
        _ = writeln!(self.out, "Built {}", output.display());
        0
    }

    /// `run`: links the program to a temporary file, runs it with
    /// `arguments` and removes it; gives the program's exit code.
    fn execute_run(
        &mut self,
        project: &project::Project,
        program: &project::Program,
        arguments: &[String],
    ) -> u8 {
        let (diagnostics, ll, _) = compile(project, program, &[], true);
        if !self.print(&diagnostics) {
            return 1;
        }
        let output = std::env::temp_dir().join(format!(
            "peeper-run-{}{}",
            std::process::id(),
            std::env::consts::EXE_SUFFIX
        ));
        if !self.link(&ll, &output) {
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

    /// Links the modules' LLVM text `ll` with the runtime into the program
    /// `output`; gives whether it did, a failure reported.
    fn link(&mut self, ll: &[llvm::Module], output: &Path) -> bool {
        let runtime = self.installation.join("runtime").join(RUNTIME_LIBRARY);
        if let Err(error) = llvm::link::link_executable(ll, &runtime, output) {
            _ = writeln!(self.out, "peeper: {error}");
            return false;
        }
        true
    }
}

/// Checks and lowers `program`, of `project`, unless loading it found an
/// error, gathering the `dumps` of its functions. With `links`, it also
/// needs the entry file's `main`. Gives every diagnostic, loading's
/// first; each module's LLVM text, when linking or a dump needs it; and
/// the dumps, to write when there is no error.
fn compile<'p>(
    project: &project::Project,
    program: &'p project::Program,
    dumps: &'p [Dump],
    links: bool,
) -> (Vec<Diagnostic>, Vec<llvm::Module<'p>>, Dumps<'p>) {
    let mut all: Vec<Diagnostic> = program.diagnostics.clone();
    let mut ll = Vec::new(); // each module's LLVM text, when it is needed
    let mut dumped = Dumps::new(program, dumps);
    // A program with a parse or load error is not checked: its errors
    // would follow from those.
    if !program.checkable {
        return (all, ll, dumped);
    }
    if links || dumped.wants_ll() {
        ll = (0..program.modules.len())
            .map(|index| {
                llvm::Module::new(program, ModuleId::from_index(index), compile::POINTER_BITS)
            })
            .collect();
    }
    // The module of the entry file, which must declare `main`.
    let entry = program
        .modules
        .iter()
        .position(|module| Path::new(&module.ast.file.path) == project.entry);
    let mut main = None; // the entry module's `main`, and whether it returns an exit code
    // Check and lower each function; its MIR goes to the dumps and the
    // LLVM text as soon as it is built.
    let results = compile::run(
        program,
        compile::POINTER_BITS,
        compile::Results::default(),
        false, // names are for editors
        |&compile::CheckedFunction {
             definition,
             signature,
             thir,
             mir,
             types,
             ..
         }| {
            if links && Some(definition.module.index()) == entry && is_main(program, definition) {
                main = Some((definition, gives_exit_code(&types[signature])));
            }
            let Some(mir) = mir else {
                return; // it has errors
            };
            dumped.add(definition, thir, mir, types);
            if let Some(module) = ll.get_mut(definition.module.index()) {
                module.add_function(definition, signature, mir, types);
            }
        },
    );
    all.extend(results.diagnostics().cloned());
    // A program that links needs a `main` that can start it.
    if links
        && !all.iter().any(Diagnostic::is_error)
        && let Some(entry) = entry
    {
        let file = &program.modules[entry].ast.file;
        match main {
            Some((main, Some(exit_code))) => ll[entry].add_entry_point(main, exit_code),
            Some((main, None)) => all.push(no_entry_point(file, name_span(program, main))),
            None => all.push(no_entry_point(file, Span::at(0))),
        }
    }
    (all, ll, dumped)
}

/// The runtime library's file in the installation's runtime folder.
const RUNTIME_LIBRARY: &str = "libpeeper_rt_v1.a";

/// Whether the function `definition` is named `main`.
fn is_main(program: &project::Program, definition: DefId) -> bool {
    let tree = &program.modules[definition.module.index()].ast;
    tree.file.slice(name_span(program, definition)) == "main"
}

/// Where the declaration `definition` writes its name.
fn name_span(program: &project::Program, definition: DefId) -> Span {
    let tree = &program.modules[definition.module.index()].ast;
    tree.decls[tree.declaration(definition.index)].name().span
}

/// Whether a `main` of type `ty` gives the program's exit code (`fn main() ->
/// i32`), gives none (`fn main()`), or cannot start a program (`None`).
fn gives_exit_code(ty: &Type) -> Option<bool> {
    match ty {
        Type::Function { parameters, result } if parameters.is_empty() => match *result {
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
/// libraries in <root>/libs and the runtime in <root>/runtime.
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
mod tests {
    use std::path::Path;
    use std::path::PathBuf;

    use super::Command;
    use super::Goal;

    fn temp_dir(test: &str) -> PathBuf {
        let root = std::env::temp_dir().join(format!("peeper-check-{}-{test}", std::process::id()));
        _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).unwrap();
        root
    }

    fn write(root: &Path, files: &[(&str, &[u8])]) {
        for (name, text) in files {
            let file = root.join(name);
            std::fs::create_dir_all(file.parent().unwrap()).unwrap();
            std::fs::write(file, text).unwrap();
        }
    }

    /// Checks `path` with the libraries of `installation`; gives the exit
    /// code and what was printed.
    fn checked(path: &Path, installation: &Path) -> (u8, String) {
        let mut command = Command {
            path: path.to_path_buf(),
            installation: installation.to_path_buf(),
            out: Vec::new(),
            colored: false,
        };
        let check = Goal::Check {
            dumps: Vec::new(),
            apply_fixes: false,
        };
        let code = command.execute(&check);
        (code, String::from_utf8(command.out).unwrap())
    }

    /// `peeper fix` writes the suggested fixes, lists them top down, and
    /// reports only the errors left, which have no fix.
    #[test]
    fn fixes_a_file() {
        let root = temp_dir("fix");
        let text = b"fn main() {\n    let count = 1;\n    let a = cout\n    let b = gone;\n}\n";
        write(&root, &[("app.peep", text)]);
        let file = root.join("app.peep");
        let mut command = Command {
            path: file.clone(),
            installation: PathBuf::new(),
            out: Vec::new(),
            colored: false,
        };
        let fix = Goal::Check {
            dumps: Vec::new(),
            apply_fixes: true,
        };
        let code = command.execute(&fix);
        let out = String::from_utf8(command.out).unwrap();
        let fixed = std::fs::read_to_string(&file).unwrap();
        assert_eq!(
            fixed,
            "fn main() {\n    let count = 1;\n    let a = cout;\n    let b = gone;\n}\n"
        );
        let first = format!("fixed {}:3:17: ", file.display());
        assert!(out.starts_with(&first), "{out}");
        assert!(out.contains("unknown name `gone`"), "{out}");
        assert_eq!(code, 1, "`gone` has no fix");
    }

    #[test]
    fn checks_a_project() {
        let root = temp_dir("project");
        write(
            &root,
            &[
                ("app/peeper.toml", b"name = \"myapp\""),
                (
                    "app/src/main.peep",
                    b"import \"myapp:util\";\nimport \"myapp:gone\";\nfn main() {}",
                ),
                ("app/src/util.peep", b"fn Util() {}"),
                ("libs/core/src/global.peep", b""),
            ],
        );
        let (code, out) = checked(&root.join("app"), &root);
        let gone = root.join("app/src/gone.peep");
        let main = root.join("app/src/main.peep");
        let want = format!(
            "[M0001]: module not found: {}\n  --> {}:2:1\n",
            gone.display(),
            main.display()
        );
        assert_eq!(code, 1);
        assert!(
            out.starts_with(&want) && out.ends_with("Compilation failed with 1 error(s)\n"),
            "{out}"
        );

        write(
            &root,
            &[("app/src/main.peep", b"import \"myapp:util\";\nfn main() {}")],
        );
        assert_eq!(checked(&root.join("app"), &root), (0, String::new()));
    }

    #[test]
    fn checks_a_single_file() {
        let root = temp_dir("single");
        write(
            &root,
            &[
                ("tool.peep", b"fn main() {}"),
                ("bad.peep", b"fn main() { a $ b; }\n"),
            ],
        );
        assert_eq!(
            checked(&root.join("tool.peep"), Path::new("")),
            (0, String::new())
        );
        let (code, out) = checked(&root.join("bad.peep"), Path::new(""));
        assert_eq!(code, 1);
        assert!(
            out.starts_with("[L0001]: illegal character \"$\"\n"),
            "{out}"
        );

        write(&root, &[("invalid.peep", b"fn f() {}\n// \xff\n")]);
        let (code, out) = checked(&root.join("invalid.peep"), Path::new(""));
        assert_eq!(code, 1);
        assert!(
            out.starts_with("[L0001]: file is not valid UTF-8\n") && out.contains(":2:4\n"),
            "{out}"
        );
        assert_eq!(checked(&root.join("missing.peep"), Path::new("")).0, 1);
    }

    #[test]
    fn folder_errors() {
        let root = temp_dir("folders");
        write(
            &root,
            &[
                ("core/peeper.toml", b"name = \"core\""),
                ("libs/core/src/x.peep", b""),
                ("empty/x", b""),
            ],
        );
        for (folder, want) in [
            ("empty", "no peeper.toml in this folder"),
            ("core", "project name `core` is taken by a bundled library"),
        ] {
            let (code, out) = checked(&root.join(folder), &root);
            assert!(code == 1 && out.contains(want), "{folder}: {code} {out}");
        }
    }

    /// `build` and `run` need the entry file's `fn main()` or `fn main() ->
    /// i32`, and report it before linking; `check` does not.
    #[test]
    fn needs_an_entry_point() {
        let root = temp_dir("entry");
        write(
            &root,
            &[
                ("none.peep", b"fn helper() {}\n"),
                ("bodyless.peep", b"fn main();\n"),
                ("takes.peep", b"fn main(x: i32) {}\n"),
                ("wide.peep", b"fn main() -> i64 { return 0; }\n"),
            ],
        );
        for (file, at) in [
            ("none.peep", ":1:1"),
            ("bodyless.peep", ":1:1"),
            ("takes.peep", ":1:4"),
            ("wide.peep", ":1:4"),
        ] {
            let path = root.join(file);
            let mut command = Command {
                path: path.clone(),
                installation: PathBuf::new(),
                out: Vec::new(),
                colored: false,
            };
            let code = command.execute(&Goal::Build { output: None });
            let out = String::from_utf8(command.out).unwrap();
            assert_eq!(code, 1, "{file}: {out}");
            assert!(
                out.starts_with("[M0006]: program entrypoint must be")
                    && out.contains(&format!("{}{at}\n", path.display())),
                "{file}: {out}"
            );
            assert_eq!(
                checked(&path, Path::new("")).0,
                0,
                "{file}: check needs no main"
            );
        }
    }
}
