//! Runs the Peeper fixtures in x_test: each folder with a peeper.toml is a
//! project whose `[test]` section says what the compiler must do with it.
//! `mode = "check"` checks it; `mode = "mir"` dumps its MIR (`peeper -dump
//! mir`) and compares the main module's with the folder's expected.mir, which
//! `PEEPER_BLESS=1` rewrites; `mode = "run"` runs it (`peeper run`) with the
//! `program_args`. `outcome` is "success" (exit code 0), "failure" (1, or any
//! other code for `run`) or, for `run`, "exit_code" with `exit_code`; the
//! output must hold each of `stdout_contains` and `stderr_contains`. The
//! compiler is `$PEEPER` if set (the bundled build/bin/peeper, which finds
//! its libraries and runtime), else the one cargo built, bundled with the
//! libraries and runtime of this checkout.

use std::path::Path;
use std::process::Command;

/// Bundles the compiler cargo built as peeper expects to be installed: in
/// <root>/bin, beside <root>/libs and <root>/runtime. Returns its path.
fn bundle() -> String {
    let checkout = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let root = Path::new(env!("CARGO_TARGET_TMPDIR")).join("bundle");
    _ = std::fs::remove_dir_all(&root);
    std::fs::create_dir_all(root.join("bin")).expect("the bundle folder is made");
    std::fs::create_dir_all(root.join("runtime")).expect("the bundle folder is made");
    let compiler = root.join("bin/peeper");
    std::fs::copy(env!("CARGO_BIN_EXE_peeper"), &compiler).expect("the compiler is copied");
    std::os::unix::fs::symlink(
        checkout.join("_builtin_library").canonicalize().unwrap(),
        root.join("libs"),
    )
    .expect("the libraries are linked");
    let object = root.join("runtime/peeper_rt.o");
    let compiled = Command::new("clang")
        .args(["-std=c11", "-O2", "-c"])
        .arg(checkout.join("runtime/peeper_rt.c"))
        .arg("-o")
        .arg(&object)
        .status()
        .expect("clang runs");
    assert!(compiled.success(), "the runtime compiles");
    let archived = Command::new("ar")
        .arg("rcs")
        .arg(root.join("runtime/libpeeper_rt_v1.a"))
        .arg(&object)
        .status()
        .expect("ar runs");
    assert!(archived.success(), "the runtime archives");
    compiler.to_string_lossy().into_owned()
}

#[test]
fn fixtures() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../x_test");
    let compiler = std::env::var("PEEPER").unwrap_or_else(|_| bundle());
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
            Some(mode @ ("check" | "mir" | "run")) => mode,
            _ => continue,
        };
        checked += 1;
        let texts = |key: &str| -> Vec<&str> {
            match test(key).and_then(toml_edit::Item::as_array) {
                Some(values) => values.iter().filter_map(toml_edit::Value::as_str).collect(),
                None => Vec::new(),
            }
        };
        let mut command = Command::new(&compiler);
        match mode {
            "mir" => command.args(["-dump", "mir"]).arg(folder),
            "run" => command.arg("run").arg(folder).args(texts("program_args")),
            _ => command.arg("check").arg(folder),
        };
        let output = command.output().expect("the compiler runs");
        let stdout = String::from_utf8_lossy(&output.stdout);
        let stderr = String::from_utf8_lossy(&output.stderr);
        let name = folder.file_name().unwrap().to_string_lossy();
        let Some(code) = output.status.code() else {
            failures.push(format!("{name}: the compiler crashed\n{stderr}"));
            continue;
        };
        let wanted_code = test("exit_code").and_then(toml_edit::Item::as_integer);
        let as_expected = match (text("outcome"), mode) {
            (Some("success"), _) => code == 0,
            (Some("failure"), "run") => code != 0,
            (Some("failure"), _) => code == 1,
            (Some("exit_code"), "run") => Some(i64::from(code)) == wanted_code,
            (other, _) => panic!("{name}: outcome {other:?} does not fit mode {mode}"),
        };
        if !as_expected {
            failures.push(format!(
                "{name}: expected {:?} {wanted_code:?}, got exit code {code}\n{stderr}",
                text("outcome")
            ));
        }
        for (key, printed) in [("stdout_contains", &stdout), ("stderr_contains", &stderr)] {
            for wanted in texts(key) {
                if !printed.contains(wanted) {
                    failures.push(format!("{name}: {key} lacks {wanted:?}\n{printed}"));
                }
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
