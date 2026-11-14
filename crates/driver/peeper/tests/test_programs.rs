//! Runs the compiler's test programs (the `test_programs` crate says what
//! each expects): a check, a dump compared with the one beside it, or a run,
//! once in a debug build and once with `-release`, whose stdout, stderr and
//! exit code must be the ones beside it, and which `run -interpret` must
//! match byte for byte. Every diagnostic reported must be marked. With
//! `PEEPER_BLESS=1`, the dumps and outputs beside each program are rewritten
//! instead. The compiler is `$PEEPER` if set (the bundled build/bin/peeper,
//! which finds its libraries, runtime and toolchain), else the one cargo
//! built, bundled with the libraries and runtime of this checkout and
//! build/toolchains/native.

use std::path::Path;
use std::process::Command;
use std::process::Output;

use llvm::link::TOOLCHAIN_FOLDER;
use model::machine::Machine;
use project::canonical_path;
use project::path_text;
use test_programs::Mode;
use test_programs::TestProgram;

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
        &root.join(project::LIBRARIES_FOLDER),
    )
    .expect("the libraries are linked");
    let toolchain = checkout.join("build").join(TOOLCHAIN_FOLDER);
    let linked = root.join(TOOLCHAIN_FOLDER);
    let parent = linked
        .parent()
        .expect("the toolchain folder is inside the root");
    std::fs::create_dir_all(parent).expect("the toolchains folder is made");
    link_folder(
        &toolchain
            .canonicalize()
            .expect("scripts/setup.sh fills build/toolchains/native"),
        &linked,
    )
    .expect("the toolchain is linked");
    let host = Machine::host().expect("this machine is a target");
    llvm::runtime::build(&root, host).expect("the runtime builds");
    // Without the 32-bit C library the `//@ target: i686-linux` programs fail,
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
/// test program names: an x86-64 Linux does, with its 32-bit C library.
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

/// Why `peeper run -interpret` did not do what the compiled program in
/// `output` did, for the project in `folder`, with the flags `target`.
fn interpreter_difference(
    compiler: &str,
    folder: &Path,
    target: Option<[&str; 2]>,
    compiled: &Output,
) -> Option<String> {
    let mut command = Command::new(compiler);
    command.args(target.iter().flatten());
    command.args(["run", "-interpret"]).arg(folder);
    let interpreted = command.output().expect("the compiler runs");
    let parts = |output: &Output| {
        let text = |bytes: &[u8]| String::from_utf8_lossy(bytes).into_owned();
        let (stdout, stderr) = (text(&output.stdout), text(&output.stderr));
        (output.status.code(), stdout, stderr)
    };
    let (compiled, interpreted) = (parts(compiled), parts(&interpreted));
    (compiled != interpreted)
        .then(|| format!("compiled: {compiled:?}\ninterpreted: {interpreted:?}"))
}

/// Compares `found`, what a run of `program` wrote or dumped, with the file
/// beside it that ends in `extension`, or rewrites that file when blessing.
fn compare(program: &TestProgram, extension: &str, found: &str, failures: &mut Vec<String>) {
    if std::env::var_os("PEEPER_BLESS").is_some() {
        let file = program.beside(extension);
        if found.is_empty() {
            _ = std::fs::remove_file(file);
        } else {
            std::fs::write(file, found).expect("an expected file is written");
        }
    } else if program.expected(extension) != found {
        failures.push(format!(
            "{}: differs from its .{extension} (PEEPER_BLESS=1 rewrites it)\n{found}",
            program.name
        ));
    }
}

#[test]
fn test_programs() {
    let compiler = std::env::var("PEEPER").unwrap_or_else(|_| bundle());
    let projects = Path::new(env!("CARGO_TARGET_TMPDIR")).join("test_programs");
    // A panic in a bundled library names its file, which is where this
    // compiler is installed.
    let installed = Path::new(&compiler).parent().and_then(Path::parent);
    let libraries = installed
        .expect("the compiler is in <root>/bin")
        .join(project::LIBRARIES_FOLDER);
    let libraries = [
        path_text(&libraries),
        path_text(&canonical_path(&libraries).expect("the libraries are installed")),
    ];
    let mut failures = Vec::new();
    let mut checked = 0;
    for program in test_programs::all() {
        if program.target.is_some() && !runs_i686() {
            eprintln!(
                "skipped {}: only x86-64 Linux runs its target",
                program.name
            );
            continue;
        }
        checked += 1;
        let folder = projects.join(&program.name);
        program.write_project(&folder);
        let builds: &[Option<&str>] = match program.mode {
            Mode::Run => &[None, Some("-release")],
            _ if program.release => &[Some("-release")],
            _ => &[None],
        };
        let target = (program.target.as_deref()).map(|name| ["-target", name]);
        for &release in builds {
            let mut command = Command::new(&compiler);
            command.args(target.iter().flatten());
            match program.mode {
                Mode::Check => command.arg("check").arg(&folder),
                Mode::Run => command.arg("run").args(release).arg(&folder),
                Mode::Dump(kind) => command.args(["-dump", kind]).args(release).arg(&folder),
            };
            let output = command.output().expect("the compiler runs");
            let stdout = String::from_utf8_lossy(&output.stdout);
            let stderr = String::from_utf8_lossy(&output.stderr);
            let name = format!("{}{}", program.name, release.map_or("", |_| " (-release)"));
            let Some(code) = output.status.code() else {
                failures.push(format!("{name}: the compiler crashed\n{stderr}"));
                continue;
            };
            let (reported, program_stderr) = test_programs::reported(&stderr, &folder);
            for problem in test_programs::mismatches(&program.marks, &reported) {
                failures.push(format!("{name}: {problem}"));
            }
            let wanted_code = match program.mode {
                Mode::Run if !program.has_errors() => i32::from(program.exit_code),
                _ => i32::from(program.has_errors()),
            };
            if code != wanted_code {
                failures.push(format!(
                    "{name}: exit code {code}, not {wanted_code}\n{stderr}"
                ));
            }
            match program.mode {
                Mode::Run => {
                    compare(&program, "out", &stdout, &mut failures);
                    let program_stderr = (libraries.iter())
                        .fold(program_stderr.to_string(), |text, path| {
                            text.replace(path, "<libraries>")
                        });
                    compare(&program, "err", &program_stderr, &mut failures);
                    if release.is_none()
                        && !program.has_errors()
                        && let Some(difference) =
                            interpreter_difference(&compiler, &folder, target, &output)
                    {
                        failures.push(format!("{name}: the interpreter differs\n{difference}"));
                    }
                }
                Mode::Dump(kind) => {
                    let dumped =
                        folder.join(format!("build/debug/dumps/{}/main.{kind}", program.name));
                    // Spelled as the compiler spells the files in it.
                    let folder_path = canonical_path(&folder).expect("the project exists");
                    match std::fs::read_to_string(&dumped) {
                        Ok(dump) => {
                            let dump = dump.replace(&path_text(&folder_path), "<fixture>");
                            compare(&program, kind, &dump, &mut failures);
                        }
                        Err(error) => failures
                            .push(format!("{name}: no dump at {}: {error}", dumped.display())),
                    }
                }
                Mode::Check => {}
            }
        }
    }
    // Each bundled library checks clean on its own: every file of it, with
    // no program around it.
    let checkout = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../..");
    let libraries = checkout.join("_builtin_library").canonicalize();
    let libraries = libraries.expect("_builtin_library exists");
    for library in std::fs::read_dir(&libraries).expect("_builtin_library lists") {
        let library = library.expect("a library folder lists").path();
        let output = (Command::new(&compiler).arg("check").arg(&library).output())
            .expect("the compiler runs");
        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            failures.push(format!("{}: does not check\n{stderr}", library.display()));
        }
    }
    assert!(checked > 0, "no test programs");
    assert!(failures.is_empty(), "{}", failures.join("\n\n"));
    if cfg!(target_os = "linux") {
        assert_release_link(&compiler);
    }
}
