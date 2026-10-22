//! The runtime library every program links with, as runtime/runtime.toml
//! describes it: its file name, which the driver looks for, and how clang and
//! an archiver build it from the C files beside that file.

use std::path::Path;
use std::path::PathBuf;
use std::process::Command;

use crate::link::call_tool;

// LIBRARY (the archive's file name, which the driver looks for), SOURCES (C
// files in the runtime folder) and FLAGS (clang's flags for each source),
// written by build.rs from runtime/runtime.toml.
include!(concat!(env!("OUT_DIR"), "/runtime.rs"));

/// Builds the library from this checkout's runtime folder into `folder`
/// and gives its path. The objects in between are removed.
pub fn build(folder: &Path) -> Result<PathBuf, String> {
    let runtime = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../runtime");
    std::fs::create_dir_all(folder)
        .map_err(|error| format!("cannot make {}: {error}", folder.display()))?;
    let library = folder.join(LIBRARY);
    _ = std::fs::remove_file(&library); // an archiver adds to an archive that is there
    let mut objects = Vec::with_capacity(SOURCES.len());
    for source in SOURCES {
        let object = folder.join(Path::new(source).with_extension("o"));
        let mut clang = Command::new("clang");
        clang
            .args(FLAGS)
            .arg("-c")
            .arg(runtime.join(source))
            .arg("-o")
            .arg(&object);
        call_tool(clang, "compile the runtime")?;
        objects.push(object);
    }
    // llvm-ar comes with LLVM; Xcode's clang has only the system's ar.
    let archiver = ["llvm-ar", "ar"]
        .into_iter()
        .find(|tool| Command::new(tool).arg("--version").output().is_ok())
        .ok_or("cannot archive the runtime: neither llvm-ar nor ar runs")?;
    let mut archive = Command::new(archiver);
    archive.arg("rcs").arg(&library).args(&objects);
    let archived = call_tool(archive, "archive the runtime");
    for object in &objects {
        _ = std::fs::remove_file(object);
    }
    archived.map(|()| library)
}
