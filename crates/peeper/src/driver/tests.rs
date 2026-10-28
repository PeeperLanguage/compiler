use std::path::Path;
use std::path::PathBuf;

use llvm::BuildMode;
use llvm::Target;

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
        target: Target::host().unwrap(),
        installation: installation.to_path_buf(),
        out: Vec::new(),
        colored: false,
    };
    let check = Goal::Check {
        dumps: Vec::new(),
        apply_fixes: false,
        mode: BuildMode::Debug,
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
        target: Target::host().unwrap(),
        installation: PathBuf::new(),
        out: Vec::new(),
        colored: false,
    };
    let fix = Goal::Check {
        dumps: Vec::new(),
        apply_fixes: true,
        mode: BuildMode::Debug,
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
            target: Target::host().unwrap(),
            installation: PathBuf::new(),
            out: Vec::new(),
            colored: false,
        };
        let code = command.execute(&Goal::Build {
            output: None,
            mode: BuildMode::Debug,
        });
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
