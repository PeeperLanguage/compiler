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
        let document =
            toml::parse_file(&folder.join("peeper.toml")).expect("a fixture's peeper.toml parses");
        let data = document.data();
        let text = |key: &str| data.get("test", key).and_then(toml::Value::as_str);
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
        let wanted = match data.get("test", "stderr_contains") {
            Some(toml::Value::Array(values)) => {
                values.iter().filter_map(toml::Value::as_str).collect()
            }
            _ => Vec::new(),
        };
        for wanted in wanted {
            if !stderr.contains(wanted) {
                failures.push(format!("{name}: stderr lacks {wanted:?}\n{stderr}"));
            }
        }
        if mode == "mir" {
            let file = folder.join("expected.mir");
            let package = data.get("default", "name").and_then(toml::Value::as_str);
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
