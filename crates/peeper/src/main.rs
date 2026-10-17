//! The Peeper compiler command.

use std::io::{IsTerminal, Write};
use std::path::{Path, PathBuf};
use std::process::ExitCode;

use diagnostics::{Severity, printer};
use project::Packages;

const USAGE: &str = "usage: peeper check [project folder | file.peep]";

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let path = match args.as_slice() {
        [command] if command == "check" => ".",
        [command, path] if command == "check" => path,
        _ => {
            eprintln!("{USAGE}");
            return ExitCode::from(2);
        }
    };
    let stderr = std::io::stderr();
    let colored = stderr.is_terminal();
    let code = check(
        Path::new(path),
        &libraries_dir(),
        &mut stderr.lock(),
        colored,
    );
    ExitCode::from(code)
}

/// Loads the program at `path`, prints its diagnostics to `out` and returns
/// the exit code: 1 when there are errors. A folder must hold peeper.toml; its
/// project starts at src/main.peep. A file is checked on its own, with no
/// project and no imports.
fn check(path: &Path, libraries: &Path, out: &mut impl Write, colored: bool) -> u8 {
    let is_dir = match std::fs::metadata(path) {
        Ok(metadata) => metadata.is_dir(),
        Err(error) => {
            _ = writeln!(out, "{}: {error}", path.display());
            return 1;
        }
    };
    let mut packages = bundled_libraries(libraries);
    let mut entry = path.to_path_buf();
    if is_dir {
        let manifest = match manifest::read(path) {
            Ok(manifest) => manifest,
            Err(error) => {
                _ = writeln!(out, "{}: {error}", path.display());
                return 1;
            }
        };
        if packages.contains_key(&manifest.name) {
            let file = manifest.root.join(manifest::FILE_NAME);
            let name = &manifest.name;
            _ = writeln!(
                out,
                "{}: project name `{name}` is taken by a bundled library",
                file.display()
            );
            return 1;
        }
        let source = manifest.root.join("src");
        entry = source.join("main.peep");
        packages.insert(manifest.name, source);
    }
    let mut program = project::load(&packages, &entry);
    let mut all = std::mem::take(&mut program.diagnostics);
    for path in &program.order {
        if let Some(module) = program.modules.get_mut(path) {
            all.append(&mut module.diagnostics);
        }
    }
    // Checking a tree with syntax errors would only add reports of the holes
    // the parser left.
    if !all.iter().any(|d| d.code.severity == Severity::Error) {
        all.extend(build::check(&program, usize::BITS));
    }
    let scan = |file: &std::sync::Arc<source::SourceFile>| lexer::scan(file).0;
    let highlight: Option<printer::TokensOf> = if colored { Some(&scan) } else { None };
    if let Err(error) = printer::print(out, &all, highlight) {
        eprintln!("peeper: cannot write diagnostics: {error}");
        return 1;
    }
    u8::from(all.iter().any(|d| d.code.severity == Severity::Error))
}

/// Maps each library folder in `libraries` to its src folder. No libraries
/// folder means no bundled libraries.
fn bundled_libraries(libraries: &Path) -> Packages {
    let Ok(entries) = std::fs::read_dir(libraries) else {
        return Packages::new();
    };
    entries
        .flatten()
        .filter(|entry| entry.path().is_dir())
        .filter_map(|entry| {
            let name = entry.file_name().into_string().ok()?;
            manifest::valid_name(&name).then(|| (name, entry.path().join("src")))
        })
        .collect()
}

/// The libs folder of the installation peeper runs from: the executable is in
/// <root>/bin and the libraries in <root>/libs.
fn libraries_dir() -> PathBuf {
    let Ok(exe) = std::env::current_exe() else {
        return PathBuf::new();
    };
    let exe = std::fs::canonicalize(&exe).unwrap_or(exe);
    let root = exe.parent().and_then(Path::parent).unwrap_or(Path::new(""));
    root.join("libs")
}

#[cfg(test)]
mod tests {
    use super::*;

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

    fn run(path: &Path, libraries: &Path) -> (u8, String) {
        let mut out = Vec::new();
        let code = check(path, libraries, &mut out, false);
        (code, String::from_utf8(out).unwrap())
    }

    #[test]
    fn checks_a_project() {
        let root = temp_dir("project");
        let libs = root.join("libs");
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
        let (code, out) = run(&root.join("app"), &libs);
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
        assert_eq!(run(&root.join("app"), &libs), (0, String::new()));
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
        let libs = root.join("libs");
        for (folder, want) in [
            ("empty", "no peeper.toml in this folder"),
            ("core", "project name `core` is taken by a bundled library"),
        ] {
            let (code, out) = run(&root.join(folder), &libs);
            assert!(code == 1 && out.contains(want), "{folder}: {code} {out}");
        }
    }
}
