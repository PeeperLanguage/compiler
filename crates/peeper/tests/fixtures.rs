//! Runs the Peeper fixtures in x_test: each folder with a peeper.toml is a
//! project whose `[test]` section says what the compiler must do with it.
//! `mode = "check"` checks it; `mode = "mir"` dumps its MIR (`peeper -dump
//! mir`) and compares the main module's with the folder's expected.mir, which
//! `PEEPER_BLESS=1` rewrites.
//! Building and running programs come with code generation. The compiler is `$PEEPER` if set (the bundled
//! build/bin/peeper, which finds its libraries), else the one cargo built.

use std::path::Path;
use std::process::Command;

#[test]
fn fixtures() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../x_test");
    let compiler =
        std::env::var("PEEPER").unwrap_or_else(|_| env!("CARGO_BIN_EXE_peeper").to_string());
    let mut folders: Vec<_> = std::fs::read_dir(&root)
        .expect("x_test exists")
        .flatten()
        .map(|entry| entry.path())
        .filter(|folder| folder.join("peeper.toml").is_file())
        .collect();
    folders.sort();
    let mut failures = Vec::new();
    let mut checked = 0;
    for folder in &folders {
        let text = std::fs::read_to_string(folder.join("peeper.toml")).expect("peeper.toml reads");
        let document: toml_edit::DocumentMut =
            text.parse().expect("a fixture's peeper.toml parses");
        let test = |key: &str| document.get("test").and_then(|test| test.get(key));
        let text = |key: &str| test(key).and_then(toml_edit::Item::as_str);
        let mode = match text("mode") {
            Some(mode @ ("check" | "mir")) => mode,
            _ => continue,
        };
        checked += 1;
        let command = if mode == "mir" {
            ["-dump", "mir"]
        } else {
            ["check", ""]
        };
        let output = Command::new(&compiler)
            .args(command.iter().filter(|arg| !arg.is_empty()))
            .arg(folder)
            .output()
            .expect("the compiler runs");
        let stderr = String::from_utf8_lossy(&output.stderr);
        let name = folder.file_name().unwrap().to_string_lossy();
        let succeeded = match output.status.code() {
            Some(0) => true,
            Some(1) => false,
            _ => {
                failures.push(format!("{name}: the compiler crashed\n{stderr}"));
                continue;
            }
        };
        let expected = match text("outcome") {
            Some("success") => true,
            Some("failure") => false,
            other => panic!("{name}: outcome {other:?} is not success or failure"),
        };
        if succeeded != expected {
            failures.push(format!(
                "{name}: expected {:?}, got\n{stderr}",
                text("outcome")
            ));
        }
        let wanted = match test("stderr_contains").and_then(toml_edit::Item::as_array) {
            Some(values) => values.iter().filter_map(toml_edit::Value::as_str).collect(),
            None => Vec::new(),
        };
        for wanted in wanted {
            if !stderr.contains(wanted) {
                failures.push(format!("{name}: stderr lacks {wanted:?}\n{stderr}"));
            }
        }
        if mode == "mir" {
            let file = folder.join("expected.mir");
            let package = document.get("name").and_then(toml_edit::Item::as_str);
            let package = package.expect("a fixture's peeper.toml has a name");
            let dumped = folder.join(format!("build/debug/dumps/{package}/main.mir"));
            let mir = std::fs::read_to_string(&dumped).unwrap_or_default();
            if std::env::var_os("PEEPER_BLESS").is_some() {
                std::fs::write(&file, &mir).expect("expected.mir is written");
            } else if std::fs::read_to_string(&file).ok() != Some(mir.clone()) {
                failures.push(format!(
                    "{name}: the MIR differs from expected.mir (PEEPER_BLESS=1 rewrites it)\n{mir}"
                ));
            }
        }
    }
    assert!(checked > 0, "no check fixtures in {}", root.display());
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
}
