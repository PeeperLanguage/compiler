//! Runs the Peeper fixtures in x_test: each folder with a peeper.toml is a
//! project whose `[test]` section says what the compiler must do with it.
//! `mode = "check"` checks it; `mode = "mir"` or `"ll"` dumps its MIR or LLVM
//! text (`peeper -dump mir`) and compares the main module's with the folder's
//! expected.mir or expected.ll, which `PEEPER_BLESS=1` rewrites (each run's
//! dump is removed first, so an old one never passes); the folder's
//! path, in debug info and `source_filename`, shows as `<fixture>` there, so
//! they read the same in every checkout; with `release = true`, the dump is of a release build (`-dump -release`).
//! `mode = "run"` runs it (`peeper run`) with the `program_args`, once in a
//! debug build and once with `-release`. `outcome` is "success" (exit code
//! 0), "failure" (1, or any other code for `run`) or, for `run`,
//! "exit_code" with `exit_code`; the
//! output must hold each of `stdout_contains` and `stderr_contains`, and the
//! interpreter must print and exit byte for byte as the debug build does. With
//! `target = "i686-linux"`, each command builds for that target (`-target
//! i686-linux`). The compiler is `$PEEPER` if set (the bundled build/bin/peeper, which finds
//! its libraries, runtime and toolchain), else the one cargo built, bundled
//! with the libraries and runtime of this checkout and build/toolchains/native.

use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use std::process::Command;
use std::process::Output;

use eval::interpreter::program::Stream;
use model::ModuleId;
use model::machine::Machine;
use project::canonical_path;
use project::path_text;

/// Bundles the compiler cargo built as peeper expects to be installed: in
/// <root>/bin, beside <root>/libs, <root>/runtime and <root>/toolchains/native,
/// the toolchain scripts/setup.sh puts in build/. Returns its path.
fn bundle() -> String {
    let checkout = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../..");
    let root = Path::new(env!("CARGO_TARGET_TMPDIR")).join("bundle");
    _ = std::fs::remove_dir_all(&root);
    std::fs::create_dir_all(root.join("bin")).expect("the bundle folder is made");
    let compiler = root.join(format!("bin/peeper{}", std::env::consts::EXE_SUFFIX));
    std::fs::copy(env!("CARGO_BIN_EXE_peeper"), &compiler).expect("the compiler is copied");
    link_folder(
        &checkout.join("_builtin_library").canonicalize().unwrap(),
        &root.join("libs"),
    )
    .expect("the libraries are linked");
    let toolchain = checkout.join("build/toolchains/native");
    std::fs::create_dir_all(root.join("toolchains")).expect("the toolchains folder is made");
    link_folder(
        &toolchain
            .canonicalize()
            .expect("scripts/setup.sh fills build/toolchains/native"),
        &root.join("toolchains/native"),
    )
    .expect("the toolchain is linked");
    let host = Machine::host().expect("this machine is a target");
    llvm::runtime::build(&root, host).expect("the runtime builds");
    // Without the 32-bit C library the `target = "i686-linux"` fixtures fail,
    // their runtime missing.
    let i686 = Machine::named("i686-linux").expect("i686-linux is a target");
    if runs_i686()
        && let Err(error) = llvm::runtime::build(&root, i686)
    {
        eprintln!("the i686 runtime is not built: {error}");
    }
    compiler.to_string_lossy().into_owned()
}

/// Makes `link` a link to the folder `original`.
fn link_folder(original: &Path, link: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    let linked = std::os::unix::fs::symlink(original, link);
    // shortcut: a folder symlink needs admin rights or Developer Mode on
    // Windows; make a junction instead once std's `junction_point` is stable.
    #[cfg(windows)]
    let linked = std::os::windows::fs::symlink_dir(original, link);
    linked
}

/// Whether this machine runs `i686-linux` programs, the only target a
/// fixture names: an x86-64 Linux does, with its 32-bit C library.
fn runs_i686() -> bool {
    Machine::host() == Machine::named("x86_64-linux")
}

/// A release program keeps only what it uses of the runtime, with no symbols
/// or debug info; a debug one keeps its debug info. Either takes `fmod` from
/// the system, so its import table names it. The names are Linux's.
fn assert_release_link(compiler: &str) {
    let folder = Path::new(env!("CARGO_TARGET_TMPDIR"));
    let source = folder.join("stripped.peep");
    let build = |text: &str, release: Option<&str>| {
        std::fs::write(&source, text).expect("the program is written");
        let program = folder.join("stripped");
        let built = Command::new(compiler)
            .arg("build")
            .args(release)
            .arg("-o")
            .arg(&program)
            .arg(&source)
            .output()
            .expect("the compiler runs");
        let errors = String::from_utf8_lossy(&built.stderr);
        assert!(built.status.success(), "{errors}");
        std::fs::read(&program).expect("the program reads")
    };
    let holds =
        |program: &[u8], name: &[u8]| program.windows(name.len()).any(|bytes| bytes == name);
    let hello = "fn main() {\n    println(\"hello\");\n}\n";
    assert!(holds(&build(hello, None), b".debug_info"));
    let release = build(hello, Some("-release"));
    assert!(!holds(&release, b".debug_info"));
    assert!(!holds(&release, b".symtab"));
    assert!(release.len() < 16_000, "{} bytes", release.len());
    let remainder = "fn left(a: f64, b: f64) -> f64 {\n    return a % b;\n}\n\
        fn main() {\n    println(left(5.5, 3.0));\n}\n";
    assert!(holds(&build(remainder, Some("-release")), b"fmod"));
}

/// Why the interpreter did not do what the compiled program in `output` did,
/// when it ran the fixture in `folder` for `target`: a run has no arguments
/// or input, so a fixture with `program_args` is not asked, and nor is one
/// that should not compile.
fn interpreter_difference(
    compiler: &str,
    folder: &Path,
    target: Option<&str>,
    output: &Output,
) -> Option<String> {
    let installation = Path::new(compiler).parent().and_then(Path::parent);
    let installation = installation.expect("`$PEEPER` is a path to bin/peeper");
    let project = project::find(folder, &installation.join("libs")).expect("the fixture loads");
    let program = project::load(&project, &HashMap::<PathBuf, String>::new());
    let entry = (program.modules.iter())
        .position(|module| Path::new(&module.ast.file.path) == project.entry)
        .expect("a run fixture has an entry file");
    let target = match target {
        Some(name) => Machine::named(name).expect("the target is a machine"),
        None => Machine::host().expect("this machine is a target"),
    };
    let (_, ran) = compile::interpret(&program, target, ModuleId::from_index(entry));
    let Some(ran) = ran else {
        return Some("it did not start".to_string());
    };
    let written = |stream| -> Vec<u8> {
        let pieces = ran.output.iter().filter(|piece| piece.stream == stream);
        pieces.flat_map(|piece| piece.text.clone()).collect()
    };
    let compiled = (output.status.code(), &output.stdout, &output.stderr);
    let interpreted = (
        Some(i32::from(ran.exit_code)),
        &written(Stream::Stdout),
        &written(Stream::Stderr),
    );
    (compiled != interpreted).then(|| {
        let text = |bytes: &Vec<u8>| String::from_utf8_lossy(bytes).into_owned();
        format!(
            "compiled: {:?} {:?} {:?}\ninterpreted: {:?} {:?} {:?}",
            compiled.0,
            text(compiled.1),
            text(compiled.2),
            interpreted.0,
            text(interpreted.1),
            text(interpreted.2),
        )
    })
}

#[test]
fn fixtures() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../x_test");
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
        let Some(mode @ ("check" | "mir" | "ll" | "run")) = text("mode") else {
            continue;
        };
        if text("target").is_some() && !runs_i686() {
            eprintln!(
                "skipped {}: only x86-64 Linux runs its target",
                folder.display()
            );
            continue;
        }
        checked += 1;
        let texts = |key: &str| -> Vec<&str> {
            match test(key).and_then(toml_edit::Item::as_array) {
                Some(values) => values.iter().filter_map(toml_edit::Value::as_str).collect(),
                None => Vec::new(),
            }
        };
        // A run fixture does the same in a debug and a release build; a
        // dump is of a release build when `release = true`.
        let release_dump = test("release").and_then(toml_edit::Item::as_bool) == Some(true);
        let builds: &[Option<&str>] = match mode {
            "run" => &[None, Some("-release")],
            _ if release_dump => &[Some("-release")],
            _ => &[None],
        };
        let target = text("target").map(|name| ["-target", name]);
        let dumped = matches!(mode, "mir" | "ll").then(|| {
            let package = document.get("name").and_then(toml_edit::Item::as_str);
            let package = package.expect("a fixture's peeper.toml has a name");
            folder.join(format!("build/debug/dumps/{package}/main.{mode}"))
        });
        for &release in builds {
            // Removed first, so only this run's dump can match.
            if let Some(dumped) = &dumped {
                _ = std::fs::remove_file(dumped);
            }
            let mut command = Command::new(&compiler);
            command.args(target.iter().flatten());
            match mode {
                "mir" | "ll" => command.args(["-dump", mode]).args(release).arg(folder),
                "run" => {
                    let program_args = texts("program_args");
                    command.arg("run").args(release).arg(folder);
                    if !program_args.is_empty() {
                        command.arg("--").args(program_args);
                    }
                    &mut command
                }
                _ => command.arg("check").arg(folder),
            };
            let output = command.output().expect("the compiler runs");
            let stdout = String::from_utf8_lossy(&output.stdout);
            let stderr = String::from_utf8_lossy(&output.stderr);
            let name = folder.file_name().unwrap().to_string_lossy();
            let name = format!("{name}{}", release.map_or("", |_| " (-release)"));
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
            if mode == "run"
                && release.is_none()
                && texts("program_args").is_empty()
                && text("outcome") != Some("failure")
                && let Some(difference) =
                    interpreter_difference(&compiler, folder, text("target"), &output)
            {
                failures.push(format!("{name}: the interpreter differs\n{difference}"));
            }
            for (key, printed) in [("stdout_contains", &stdout), ("stderr_contains", &stderr)] {
                for wanted in texts(key) {
                    if !printed.contains(wanted) {
                        failures.push(format!("{name}: {key} lacks {wanted:?}\n{printed}"));
                    }
                }
            }
            if let Some(dumped) = &dumped {
                let file = folder.join(format!("expected.{mode}"));
                // Spelled as the compiler spells the files in it.
                let folder_path = canonical_path(folder).expect("the fixture folder exists");
                let dump = match std::fs::read_to_string(dumped) {
                    Ok(dump) => dump.replace(&path_text(&folder_path), "<fixture>"),
                    Err(error) => {
                        failures.push(format!("{name}: no dump at {}: {error}", dumped.display()));
                        continue;
                    }
                };
                if std::env::var_os("PEEPER_BLESS").is_some() {
                    std::fs::write(&file, &dump).expect("the expected dump is written");
                } else if std::fs::read_to_string(&file).ok() != Some(dump.clone()) {
                    failures.push(format!(
                        "{name}: the dump differs from expected.{mode} (PEEPER_BLESS=1 rewrites it)\n{dump}"
                    ));
                }
            }
        }
    }
    assert!(checked > 0, "no check fixtures in {}", root.display());
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
    if cfg!(target_os = "linux") {
        assert_release_link(&compiler);
    }
}
