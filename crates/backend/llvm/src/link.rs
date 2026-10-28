//! Turning a program's LLVM modules into an executable: clang compiles the
//! modules to object files, one clang per core at once, and links the objects
//! with the runtime library. Clang is the installation's own (`tool`), never
//! one on the PATH.

use std::num::NonZero;
use std::path::Path;
use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::AtomicUsize;
use std::sync::atomic::Ordering;
use std::time::SystemTime;
use std::time::UNIX_EPOCH;

use model::machine::Machine;

use crate::BuildMode;
use crate::Module;
use crate::clang_flag;
use crate::runtime::library;

/// Compiles each of `modules` that has a function for `target`, several at
/// once, optimized in a release `mode`, and links the objects with the
/// runtime library built for it into the program `output`, with the clang
/// and runtime of `installation`. The files in between live in a temporary
/// folder, removed after.
pub fn link_executable(
    modules: &[Module],
    mode: BuildMode,
    target: &Machine,
    installation: &Path,
    output: &Path,
) -> Result<(), String> {
    let runtime = library(target, installation);
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
    let clang = tool(installation, "clang");
    let linked = compile_modules(modules, mode, target, &clang, &folder)
        .and_then(|objects| link(&objects, target, &clang, &runtime, output));
    _ = std::fs::remove_dir_all(&folder);
    linked
}

/// Links `objects` with the runtime library `runtime` into the program
/// `output` for `target`, with `clang`. The program is linked beside `output` and then
/// renamed to it, so a failed link leaves no half-written program.
fn link(
    objects: &[PathBuf],
    target: &Machine,
    clang: &Path,
    runtime: &Path,
    output: &Path,
) -> Result<(), String> {
    let name = output.file_name().unwrap_or_default().to_string_lossy();
    let staged = output.with_file_name(format!(".{name}-link-{}", std::process::id()));
    let mut linker = Command::new(clang);
    // The toolchain's own linker; without this clang runs the `ld` on the PATH.
    linker.arg("-fuse-ld=lld");
    linker.args(clang_flag(target)).args(objects).arg(runtime);
    if target.os != "windows" {
        linker.arg("-lm"); // LLVM turns a float `%` into a call of `fmod`
    }
    linker.arg("-o").arg(&staged);
    let linked = call_tool(linker, "link the program").and_then(|()| {
        std::fs::rename(&staged, output)
            .map_err(|error| format!("cannot write {}: {error}", output.display()))
    });
    _ = std::fs::remove_file(&staged);
    linked
}

/// Compiles each of `modules` that has a function into an object file for
/// `target` in `folder` with `clang`, several at once, optimized in a release `mode`,
/// and gives the objects in module order. Up to one
/// clang runs per core; each worker takes the next module no one has taken. When
/// modules fail, the first one's error is given, whichever finished first.
fn compile_modules(
    modules: &[Module],
    mode: BuildMode,
    target: &Machine,
    clang: &Path,
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
                        if let Err(error) = std::fs::write(&file, module.text()) {
                            let error = format!("cannot write {}: {error}", file.display());
                            failures.push((index, error));
                            continue;
                        }
                        let mut compiler = Command::new(clang);
                        compiler
                            .args(clang_flag(target))
                            .args(["-c", optimization, "-x", "ir"])
                            .arg(&file)
                            .arg("-o")
                            .arg(&objects[index]);
                        if let Err(error) = call_tool(compiler, "compile a module") {
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

/// The LLVM tool `name` of `installation`, in its toolchain folder; peeper
/// uses only its own, which must be LLVM 23 or newer (docs/ir-design.md).
pub(crate) fn tool(installation: &Path, name: &str) -> PathBuf {
    installation.join("toolchains/native/bin").join(name)
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
