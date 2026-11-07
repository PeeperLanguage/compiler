//! Lists the files of the bundled libraries for `main.rs` to embed, each
//! peeper.toml and source file: a web page has no disk to find them on, and a
//! file added to a library is then embedded with no other change.

use std::fmt::Write;
use std::path::Path;
use std::path::PathBuf;

fn main() {
    let libraries = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../_builtin_library");
    let libraries = libraries
        .canonicalize()
        .expect("the bundled libraries are in _builtin_library");
    // A folder makes Cargo look at every file in it.
    println!("cargo::rerun-if-changed={}", libraries.display());
    let mut files = Vec::new();
    add_files(&libraries, &mut files);
    files.sort(); // the same list on every machine
    let mut text = String::from("&[\n");
    for file in files {
        let relative = file
            .strip_prefix(&libraries)
            .expect("a library file is in the libraries folder");
        // Written with `/` on every system, as the page's paths are.
        let parts: Vec<_> = relative.iter().map(|part| part.to_string_lossy()).collect();
        _ = writeln!(
            text,
            "    ({:?}, include_str!({:?})),",
            parts.join("/"),
            file
        );
    }
    text.push(']');
    let out = PathBuf::from(std::env::var_os("OUT_DIR").expect("Cargo gives OUT_DIR"));
    std::fs::write(out.join("library_files.rs"), text).expect("the list is written");
}

/// Adds each peeper.toml and source file in `folder` and the folders in it to
/// `files`. Any other file, such as an editor's swap file, is left out: it
/// may not be text, which `include_str!` needs.
fn add_files(folder: &Path, files: &mut Vec<PathBuf>) {
    let entries =
        std::fs::read_dir(folder).unwrap_or_else(|error| panic!("{}: {error}", folder.display()));
    // A link to a folder is not followed, so a link back up ends.
    for entry in entries {
        let entry = entry.expect("a library folder lists");
        let path = entry.path();
        if entry.file_type().is_ok_and(|kind| kind.is_dir()) {
            add_files(&path, files);
        } else if path.file_name().is_some_and(|name| name == "peeper.toml")
            || path
                .extension()
                .is_some_and(|extension| extension == "peep")
        {
            files.push(path);
        }
    }
}
