//! What a `peeper` command does: load the program, check and compile it,
//! apply the fixes asked for, print its diagnostics, write the dumps asked
//! for, and build or run it.

use std::collections::HashMap;
use std::io::Write;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use arena::Id;
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

/// What a command does with a program once it is checked.
pub enum Goal<'a> {
    /// Write the `dumps`; with `apply_fixes`, write the fixes the diagnostics
    /// suggest to the project's files, list them, and check again to report
    /// what is left.
    Check {
        dumps: &'a [Dump],
        apply_fixes: bool,
    },
    /// Link the program into `output`: by default the program's name, in the
    /// current folder.
    Build { output: Option<&'a Path> },
    /// Link it to a temporary file and run it with `arguments`.
    Run { arguments: &'a [String] },
}

/// Loads the program at `path`, prints its diagnostics to `out` and does what
/// `goal` asks. The result is the exit code: 1 when there are errors, else 0,
/// or the program's own when it ran. A folder must hold peeper.toml; its
/// project starts at src/main.peep. A file is checked on its own, with no
/// project and no imports. Dumps go to build/debug/dumps in the project
/// folder, or beside the file, when the program has no errors. The bundled
/// libraries and runtime are in `installation` (`installation_dir`).
pub fn run(
    path: &Path,
    installation: &Path,
    out: &mut impl Write,
    colored: bool,
    goal: &Goal,
) -> u8 {
    let project = match project::find(path, &installation.join("libs")) {
        Ok(project) => project,
        Err(error) => {
            _ = writeln!(out, "{error}");
            return 1;
        }
    };
    let program = project::load(&project, &HashMap::<PathBuf, String>::new());
    let mut all: Vec<Diagnostic> = program.load_diagnostics().cloned().collect();
    let failed = |all: &[Diagnostic]| all.iter().any(Diagnostic::is_error);
    let dumps = match goal {
        Goal::Check { dumps, .. } => dumps,
        Goal::Build { .. } | Goal::Run { .. } => &[][..],
    };
    let links = !matches!(goal, Goal::Check { .. });
    let pointer_bits = usize::BITS;
    let mut ll = Vec::new();
    if program.checkable() {
        let mut dumped = Dumps::new(&program, dumps);
        if links || dumped.wants_ll() {
            ll = (0..program.modules.len())
                .map(|index| llvm::Module::new(&program, ModuleId::from_index(index), pointer_bits))
                .collect();
        }
        let entry = program
            .modules
            .iter()
            .position(|module| Path::new(&module.ast.file.path) == project.entry);
        let mut main = None; // the entry module's `main`, and whether it returns an exit code
        let results = build::compile(
            &program,
            pointer_bits,
            build::Results::default(),
            false, // names are for editors
            |definition, ty, thir, _, mir, types| {
                if links
                    && Some(definition.module.index()) == entry
                    && is_main(&program, definition)
                {
                    main = Some((definition, gives_exit_code(&types[ty])));
                }
                let Some(mir) = mir else {
                    return; // it has errors
                };
                dumped.add(definition, thir, mir, types);
                if let Some(module) = ll.get_mut(definition.module.index()) {
                    module.function(definition, ty, mir, types);
                }
            },
        );
        all.extend(results.diagnostics().cloned());
        if links
            && !failed(&all)
            && let Some(entry) = entry
        {
            let file = &program.modules[entry].ast.file;
            match main {
                Some((main, Some(exit_code))) => ll[entry].entry_point(main, exit_code),
                Some((main, None)) => all.push(no_entry_point(file, name_span(&program, main))),
                None => all.push(no_entry_point(file, Span::at(0))),
            }
        }
        if !failed(&all)
            && let Err(error) = dumped.write(&project.root.join("build/debug/dumps"), &ll, out)
        {
            _ = writeln!(out, "peeper: cannot write a dump: {error}");
            return 1;
        }
    }
    if let Goal::Check {
        dumps,
        apply_fixes: true,
    } = goal
    {
        let fixes = fix::chosen(&all, &project.root);
        if !fixes.is_empty() {
            if let Err(error) = fix::write(&fixes, out) {
                _ = writeln!(out, "peeper: cannot write a fix: {error}");
                return 1;
            }
            let again = Goal::Check {
                dumps,
                apply_fixes: false,
            };
            return run(path, installation, out, colored, &again);
        }
    }
    let scan = |file: &Arc<SourceFile>| lexer::scan(file).0;
    let highlight: Option<printer::TokensOf> = if colored { Some(&scan) } else { None };
    if let Err(error) = printer::print(out, &all, highlight) {
        eprintln!("peeper: cannot write diagnostics: {error}");
        return 1;
    }
    if failed(&all) {
        return 1;
    }
    link(goal, &project, &ll, installation, out)
}

/// Links the program, whose modules' LLVM text is in `ll`, as `goal` asks,
/// and runs it for `Goal::Run`; gives the exit code.
fn link(
    goal: &Goal,
    project: &project::Project,
    ll: &[llvm::Module],
    installation: &Path,
    out: &mut impl Write,
) -> u8 {
    let output = match goal {
        Goal::Check { .. } => return 0,
        Goal::Build {
            output: Some(output),
        } => output.to_path_buf(),
        Goal::Build { output: None } => {
            PathBuf::from(format!("{}{}", project.name, std::env::consts::EXE_SUFFIX))
        }
        Goal::Run { .. } => std::env::temp_dir().join(format!(
            "peeper-run-{}{}",
            std::process::id(),
            std::env::consts::EXE_SUFFIX
        )),
    };
    let texts: Vec<String> = ll
        .iter()
        .filter(|module| !module.is_empty())
        .map(llvm::Module::text)
        .collect();
    let runtime = installation.join("runtime").join(RUNTIME_LIBRARY);
    if let Err(error) = llvm::link::executable(&texts, &runtime, &output) {
        _ = writeln!(out, "peeper: {error}");
        return 1;
    }
    let Goal::Run { arguments } = goal else {
        _ = writeln!(out, "Built {}", output.display());
        return 0;
    };
    _ = out.flush();
    let status = std::process::Command::new(&output)
        .args(*arguments)
        .status();
    _ = std::fs::remove_file(&output);
    match status {
        Ok(status) => exit_code_of(status),
        Err(error) => {
            _ = writeln!(out, "peeper: cannot run the program: {error}");
            1
        }
    }
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
    tree.decls[Id::from_index(definition.index as usize)]
        .name()
        .span
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
    fn run(path: &Path, installation: &Path) -> (u8, String) {
        let check = Goal::Check {
            dumps: &[],
            apply_fixes: false,
        };
        let mut out = Vec::new();
        let code = super::run(path, installation, &mut out, false, &check);
        (code, String::from_utf8(out).unwrap())
    }

    /// `peeper fix` writes the suggested fixes, lists them top down, and
    /// reports only the errors left, which have no fix.
    #[test]
    fn fixes_a_file() {
        let root = temp_dir("fix");
        let text = b"fn main() {\n    let count = 1;\n    let a = cout\n    let b = gone;\n}\n";
        write(&root, &[("app.peep", text)]);
        let file = root.join("app.peep");
        let mut out = Vec::new();
        let fix = Goal::Check {
            dumps: &[],
            apply_fixes: true,
        };
        let code = super::run(&file, Path::new(""), &mut out, false, &fix);
        let out = String::from_utf8(out).unwrap();
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
        let (code, out) = run(&root.join("app"), &root);
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
        assert_eq!(run(&root.join("app"), &root), (0, String::new()));
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
            run(&root.join("tool.peep"), Path::new("")),
            (0, String::new())
        );
        let (code, out) = run(&root.join("bad.peep"), Path::new(""));
        assert_eq!(code, 1);
        assert!(
            out.starts_with("[L0001]: illegal character \"$\"\n"),
            "{out}"
        );

        write(&root, &[("invalid.peep", b"fn f() {}\n// \xff\n")]);
        let (code, out) = run(&root.join("invalid.peep"), Path::new(""));
        assert_eq!(code, 1);
        assert!(
            out.starts_with("[L0001]: file is not valid UTF-8\n") && out.contains(":2:4\n"),
            "{out}"
        );
        assert_eq!(run(&root.join("missing.peep"), Path::new("")).0, 1);
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
            let (code, out) = run(&root.join(folder), &root);
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
        let build = Goal::Build { output: None };
        for (file, at) in [
            ("none.peep", ":1:1"),
            ("bodyless.peep", ":1:1"),
            ("takes.peep", ":1:4"),
            ("wide.peep", ":1:4"),
        ] {
            let path = root.join(file);
            let mut out = Vec::new();
            let code = super::run(&path, Path::new(""), &mut out, false, &build);
            let out = String::from_utf8(out).unwrap();
            assert_eq!(code, 1, "{file}: {out}");
            assert!(
                out.starts_with("[M0006]: program entrypoint must be")
                    && out.contains(&format!("{}{at}\n", path.display())),
                "{file}: {out}"
            );
            assert_eq!(
                run(&path, Path::new("")).0,
                0,
                "{file}: check needs no main"
            );
        }
    }
}
