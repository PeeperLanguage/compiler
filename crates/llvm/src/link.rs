//! Turning a program's LLVM modules into an executable: clang compiles each
//! module to an object file and links the objects with the runtime library.
//! Clang is the one on the PATH until peeper ships its own toolchain.

use std::path::Path;
use std::process::Command;
use std::time::SystemTime;
use std::time::UNIX_EPOCH;

use crate::Module;

/// Compiles each of `modules` that has a function and links the objects with
/// the runtime library `runtime` into the program `output`. The files in between
/// live in a temporary folder, removed after. The program is linked beside
/// `output` and then renamed to it, so a failed link leaves no half-written
/// program.
pub fn executable(modules: &[Module], runtime: &Path, output: &Path) -> Result<(), String> {
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
    let linked = compile_and_link(modules, runtime, output, &folder);
    _ = std::fs::remove_dir_all(&folder);
    linked
}

fn compile_and_link(
    modules: &[Module],
    runtime: &Path,
    output: &Path,
    folder: &Path,
) -> Result<(), String> {
    let mut objects = Vec::with_capacity(modules.len());
    let functions = modules.iter().filter(|module| !module.is_empty());
    for (index, module) in functions.enumerate() {
        let file = folder.join(format!("module_{index}.ll"));
        let object = folder.join(format!("module_{index}.o"));
        std::fs::write(&file, module.text())
            .map_err(|error| format!("cannot write {}: {error}", file.display()))?;
        let mut clang = Command::new("clang");
        clang
            .args(["-c", "-x", "ir"])
            .arg(&file)
            .arg("-o")
            .arg(&object);
        run(clang, "compile a module")?;
        objects.push(object);
    }
    let name = output.file_name().unwrap_or_default().to_string_lossy();
    let staged = output.with_file_name(format!(".{name}-link-{}", std::process::id()));
    let mut clang = Command::new("clang");
    clang.args(&objects).arg(runtime);
    if !cfg!(windows) {
        clang.arg("-lm"); // LLVM turns a float `%` into a call of `fmod`
    }
    clang.arg("-o").arg(&staged);
    let linked = run(clang, "link the program").and_then(|()| {
        std::fs::rename(&staged, output)
            .map_err(|error| format!("cannot write {}: {error}", output.display()))
    });
    _ = std::fs::remove_file(&staged);
    linked
}

/// Runs `command`; what it printed is shown only when it fails.
fn run(mut command: Command, action: &str) -> Result<(), String> {
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
