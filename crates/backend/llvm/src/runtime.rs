//! The runtime library every program links with, as runtime/runtime.toml
//! describes it: its file name, which the driver looks for, and how `rustc`
//! builds it from the Rust file beside that file.

use std::path::Path;
use std::path::PathBuf;
use std::process::Command;

use model::machine::Machine;

use crate::link::call_tool;

// LIBRARY (the archive's file name, which the driver looks for), SOURCE (the
// Rust file in the runtime folder) and FLAGS (rustc's flags), written by
// build.rs from runtime/runtime.toml.
include!(concat!(env!("OUT_DIR"), "/runtime.rs"));

/// Builds the library for `target` from this checkout's runtime folder with
/// `rustc`, into the runtime folder of `installation`, where `library` finds
/// it, and gives its path. It is built in a folder of this process's and then
/// renamed into place, so a failed build keeps the library that was there, and
/// builds at once share no files; that folder is removed.
pub fn build(installation: &Path, target: &Machine) -> Result<PathBuf, String> {
    let library = library(target, installation);
    let folder = library.parent().expect("a library is in a folder");
    let sources = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../runtime");
    let staging = folder.join(format!(".build-{}", std::process::id()));
    std::fs::create_dir_all(&staging)
        .map_err(|error| format!("cannot make {}: {error}", staging.display()))?;
    let staged = staging.join(LIBRARY);
    let built = (|| {
        let mut rustc = Command::new("rustc");
        rustc
            .args(["--crate-name", "peeper_rt", "--crate-type", "staticlib"])
            .args(["--target", target.rust_triple])
            .args(FLAGS)
            .arg(sources.join(SOURCE))
            .arg("-o")
            .arg(&staged);
        call_tool(rustc, "compile the runtime")?;
        std::fs::rename(&staged, &library)
            .map_err(|error| format!("cannot write {}: {error}", library.display()))
    })();
    _ = std::fs::remove_dir_all(&staging);
    built.map(|()| library)
}

/// The runtime library of `target` in `installation`: in its folder in the
/// installation's runtime folder.
pub(crate) fn library(target: &Machine, installation: &Path) -> PathBuf {
    installation.join("runtime").join(target.name).join(LIBRARY)
}
