//! Runs the Peeper fixtures in x_test: each folder with a peeper.toml is a
//! project whose `[test]` section says what the compiler must do with it.
//! Only `mode = "check"` runs for now; building and running programs come with
//! code generation. The compiler is `$PEEPER` if set (the bundled
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
        let data =
            toml::parse_file(&folder.join("peeper.toml")).expect("a fixture's peeper.toml parses");
        let text = |key: &str| data.get("test", key).and_then(toml::Value::as_str);
        if text("mode") != Some("check") {
            continue;
        }
        checked += 1;
        let output = Command::new(&compiler)
            .arg("check")
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
    }
    assert!(checked > 0, "no check fixtures in {}", root.display());
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
}
