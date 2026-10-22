//! Turning a program's LLVM modules into an executable: clang compiles the
//! modules to object files, one clang per core at once, and links the objects
//! with the runtime library.
//! Clang is the one on the PATH until peeper ships its own toolchain.

use std::num::NonZero;
use std::path::Path;
use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::AtomicUsize;
use std::sync::atomic::Ordering;
use std::time::SystemTime;
use std::time::UNIX_EPOCH;

use crate::BuildMode;
use crate::Module;

/// Compiles each of `modules` that has a function, several at once, optimized
/// in a release `mode`, and links the objects with the runtime library
/// `runtime` into the program `output`. The files in between live in a
/// temporary folder, removed after.
pub fn link_executable(
    modules: &[Module],
    mode: BuildMode,
    runtime: &Path,
    output: &Path,
) -> Result<(), String> {
    if !runtime.is_file() {
        return Err(format!(
            "the runtime library is missing: {}",
            runtime.display()
        ));
    }
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |time| time.subsec_nanos());
    let folder = std::env::temp_dir().join(format!("peeper-link-{}-{nanos}", std::process::id()));
    std::fs::create_dir_all(&folder)
        .map_err(|error| format!("cannot make {}: {error}", folder.display()))?;
    let linked =
        compile_modules(modules, mode, &folder).and_then(|objects| link(&objects, runtime, output));
    _ = std::fs::remove_dir_all(&folder);
    linked
}

/// Links `objects` with the runtime library `runtime` into the program
/// `output`. The program is linked beside `output` and then renamed to it, so
/// a failed link leaves no half-written program.
fn link(objects: &[PathBuf], runtime: &Path, output: &Path) -> Result<(), String> {
    let name = output.file_name().unwrap_or_default().to_string_lossy();
    let staged = output.with_file_name(format!(".{name}-link-{}", std::process::id()));
    let mut clang = Command::new("clang");
    clang.args(objects).arg(runtime);
    if !cfg!(windows) {
        clang.arg("-lm"); // LLVM turns a float `%` into a call of `fmod`
    }
    clang.arg("-o").arg(&staged);
    let linked = call_tool(clang, "link the program").and_then(|()| {
        std::fs::rename(&staged, output)
            .map_err(|error| format!("cannot write {}: {error}", output.display()))
    });
    _ = std::fs::remove_file(&staged);
    linked
}

/// Compiles each of `modules` that has a function into an object file in
/// `folder`, several at once, optimized in a release `mode`, and gives the
/// objects in module order. Up to one
/// clang runs per core; each worker takes the next module no one has taken. When
/// modules fail, the first one's error is given, whichever finished first.
fn compile_modules(
    modules: &[Module],
    mode: BuildMode,
    folder: &Path,
) -> Result<Vec<PathBuf>, String> {
    let optimization = match mode {
        BuildMode::Debug => "-O0",
        BuildMode::Release => "-O2",
    };
    let modules: Vec<&Module> = modules.iter().filter(|module| !module.is_empty()).collect();
    let objects: Vec<PathBuf> = (0..modules.len())
        .map(|index| folder.join(format!("module_{index}.o")))
        .collect();
    let next = AtomicUsize::new(0);
    let workers = std::thread::available_parallelism().map_or(1, NonZero::get);
    let failure = std::thread::scope(|scope| {
        let handles: Vec<_> = (0..workers.min(modules.len()))
            .map(|_| {
                scope.spawn(|| {
                    let mut failures = Vec::new();
                    loop {
                        let index = next.fetch_add(1, Ordering::Relaxed);
                        let Some(module) = modules.get(index) else {
                            return failures;
                        };
                        let file = folder.join(format!("module_{index}.ll"));
                        let compiled = std::fs::write(&file, module.text())
                            .map_err(|error| format!("cannot write {}: {error}", file.display()))
                            .and_then(|()| {
                                let mut clang = Command::new("clang");
                                clang
                                    .args(["-c", optimization, "-x", "ir"])
                                    .arg(&file)
                                    .arg("-o")
                                    .arg(&objects[index]);
                                call_tool(clang, "compile a module")
                            });
                        if let Err(error) = compiled {
                            failures.push((index, error));
                        }
                    }
                })
            })
            .collect(); // every worker starts before any is waited for
        handles
            .into_iter()
            .flat_map(|handle| handle.join().expect("a clang worker does not panic"))
            .min_by_key(|&(index, _)| index)
    });
    match failure {
        Some((_, error)) => Err(error),
        None => Ok(objects),
    }
}

/// Runs `command`, a clang or archiver call; what it printed is shown only
/// when it fails.
pub(crate) fn call_tool(mut command: Command, action: &str) -> Result<(), String> {
    let program = command.get_program().to_string_lossy().into_owned();
    let output = command
        .output()
        .map_err(|error| format!("cannot {action}: cannot run {program}: {error}"))?;
    if output.status.success() {
        return Ok(());
    }
    let printed = String::from_utf8_lossy(&output.stderr);
    Err(format!(
        "cannot {action}: {program} failed ({})\n{}",
        output.status,
        printed.trim_end()
    ))
}
