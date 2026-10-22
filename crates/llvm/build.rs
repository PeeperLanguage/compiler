//! Reads runtime/runtime.toml when the compiler is built and writes its values
//! as constants for src/runtime.rs, so the compiler never reads the file when
//! it runs. A source the file names but the folder lacks stops the build.

use std::fmt::Write;
use std::path::Path;

fn main() {
    let runtime = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../runtime");
    let file = runtime.join("runtime.toml");
    println!("cargo:rerun-if-changed={}", file.display());
    let text = std::fs::read_to_string(&file)
        .unwrap_or_else(|error| panic!("cannot read {}: {error}", file.display()));
    let document: toml_edit::DocumentMut = text
        .parse()
        .unwrap_or_else(|error| panic!("{}: {error}", file.display()));
    let library = document
        .get("library")
        .and_then(toml_edit::Item::as_str)
        .unwrap_or_else(|| panic!("{}: `library` must be a string", file.display()));
    let texts = |key: &str| -> Vec<&str> {
        let list = document.get(key).and_then(toml_edit::Item::as_array);
        let list = list.unwrap_or_else(|| panic!("{}: `{key}` must be a list", file.display()));
        list.iter()
            .map(|value| {
                value.as_str().unwrap_or_else(|| {
                    panic!("{}: each of `{key}` must be a string", file.display())
                })
            })
            .collect()
    };
    let sources = texts("sources");
    for source in &sources {
        assert!(
            runtime.join(source).is_file(),
            "{}: source {source} is not in {}",
            file.display(),
            runtime.display()
        );
    }
    let mut constants = String::new();
    _ = writeln!(constants, "pub const LIBRARY: &str = {library:?};");
    _ = writeln!(constants, "const SOURCES: &[&str] = &{sources:?};");
    _ = writeln!(constants, "const FLAGS: &[&str] = &{:?};", texts("flags"));
    let out = Path::new(&std::env::var("OUT_DIR").expect("cargo sets OUT_DIR")).join("runtime.rs");
    std::fs::write(&out, constants)
        .unwrap_or_else(|error| panic!("cannot write {}: {error}", out.display()));
}
