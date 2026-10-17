//! What a `peeper` command does: load the program, check and compile it,
//! print its diagnostics and write the dumps asked for.

use std::io::Write;
use std::path::Path;
use std::path::PathBuf;

use diagnostics::Diagnostic;
use diagnostics::Severity;
use diagnostics::printer;
use project::Packages;

use crate::dump::Dump;
use crate::dump::Dumps;

/// Loads the program at `path`, prints its diagnostics to `out` and returns
/// the exit code: 1 when there are errors. A folder must hold peeper.toml; its
/// project starts at src/main.peep. A file is checked on its own, with no
/// project and no imports. A program without errors gets the `dumps` written
/// to build/debug/dumps in the project folder, or beside the file.
pub fn run(
    path: &Path,
    libraries: &Path,
    out: &mut impl Write,
    colored: bool,
    dumps: &[Dump],
) -> u8 {
    let is_dir = match std::fs::metadata(path) {
        Ok(metadata) => metadata.is_dir(),
        Err(error) => {
            _ = writeln!(out, "{}: {error}", path.display());
            return 1;
        }
    };
    let mut packages = bundled_libraries(libraries);
    let mut entry = path.to_path_buf();
    let mut root = path.parent().unwrap_or(Path::new("")).to_path_buf();
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
        root = manifest.root;
    }
    let mut program = project::load(&packages, &entry);
    let mut all = std::mem::take(&mut program.diagnostics);
    for path in &program.order {
        if let Some(module) = program.modules.get_mut(path) {
            all.append(&mut module.diagnostics);
        }
    }
    let failed = |all: &[Diagnostic]| all.iter().any(|d| d.code.severity == Severity::Error);
    // Checking a tree with syntax errors would only add reports of the holes
    // the parser left.
    if !failed(&all) {
        let mut dumped = Dumps::new(&program, dumps);
        all.extend(build::compile(
            &program,
            usize::BITS,
            |definition, thir, mir, types| dumped.add(definition, thir, mir, types),
        ));
        if !failed(&all)
            && let Err(error) = dumped.write(&root.join("build/debug/dumps"), out)
        {
            _ = writeln!(out, "peeper: cannot write a dump: {error}");
            return 1;
        }
    }
    let scan = |file: &std::sync::Arc<source::SourceFile>| lexer::scan(file).0;
    let highlight: Option<printer::TokensOf> = if colored { Some(&scan) } else { None };
    if let Err(error) = printer::print(out, &all, highlight) {
        eprintln!("peeper: cannot write diagnostics: {error}");
        return 1;
    }
    u8::from(failed(&all))
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
pub fn libraries_dir() -> PathBuf {
    let Ok(exe) = std::env::current_exe() else {
        return PathBuf::new();
    };
    let exe = std::fs::canonicalize(&exe).unwrap_or(exe);
    let root = exe.parent().and_then(Path::parent).unwrap_or(Path::new(""));
    root.join("libs")
}

#[cfg(test)]
mod tests {
    use std::path::Path;
    use std::path::PathBuf;

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
        let code = super::run(path, libraries, &mut out, false, &[]);
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
