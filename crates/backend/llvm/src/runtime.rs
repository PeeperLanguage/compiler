//! The runtime library every program links with, as runtime/runtime.toml
//! describes it: its file name, which the driver looks for, and how clang and
//! an archiver build it from the C files beside that file.

use std::path::Path;
use std::path::PathBuf;
use std::process::Command;

use model::machine::Machine;

use crate::clang_flag;
use crate::link::call_tool;
use crate::link::sdk_flags;
use crate::link::tool;

// LIBRARY (the archive's file name, which the driver looks for), SOURCES (C
// files in the runtime folder) and FLAGS (clang's flags for each source),
// written by build.rs from runtime/runtime.toml.
include!(concat!(env!("OUT_DIR"), "/runtime.rs"));

/// Builds the library for `target` from this checkout's runtime folder,
/// with the clang and llvm-ar of `installation`, into its runtime folder,
/// where `library` finds it, and gives its path. The objects in between are
/// removed.
pub fn build(installation: &Path, target: &Machine) -> Result<PathBuf, String> {
    let library = library(target, installation);
    let folder = library.parent().expect("a library is in a folder");
    let sources = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../runtime");
    std::fs::create_dir_all(folder)
        .map_err(|error| format!("cannot make {}: {error}", folder.display()))?;
    _ = std::fs::remove_file(&library); // an archiver adds to an archive that is there
    let sdk = sdk_flags(target)?;
    let mut objects = Vec::with_capacity(SOURCES.len());
    for source in SOURCES {
        let object = folder.join(Path::new(source).with_extension("o"));
        let mut clang = Command::new(tool(installation, "clang"));
        clang
            .args(&sdk)
            .args(clang_flag(target))
            .args(FLAGS)
            .arg("-c")
            .arg(sources.join(source))
            .arg("-o")
            .arg(&object);
        call_tool(clang, "compile the runtime")?;
        objects.push(object);
    }
    let mut archive = Command::new(tool(installation, "llvm-ar"));
    archive.arg("rcs").arg(&library).args(&objects);
    let archived = call_tool(archive, "archive the runtime");
    for object in &objects {
        _ = std::fs::remove_file(object);
    }
    archived.map(|()| library)
}

/// The runtime library of `target` in `installation`: in its folder in the
/// installation's runtime folder.
pub(crate) fn library(target: &Machine, installation: &Path) -> PathBuf {
    installation.join("runtime").join(target.name).join(LIBRARY)
}
