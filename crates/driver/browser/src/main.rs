//! The checker a web page runs, built for `wasm32-wasip1`: reads the page's
//! files from stdin as a JSON object from file name to text, and writes the
//! program's diagnostics to stdout as a JSON list. The program starts at
//! main.peep, and a file imports another as "playground:name". Nothing is read
//! from a disk. The compiler can overflow wasm's stack on very deep code, so a
//! page runs a new instance for each check, and one crash loses nothing.

use std::collections::HashMap;
use std::io::Read;
use std::path::Path;
use std::path::PathBuf;
use std::process::ExitCode;

use diagnostics::Label;
use diagnostics::Severity;
use model::machine::Machine;
use project::Project;
use serde_json::Value;
use serde_json::json;

/// The package the page's files make up.
const PACKAGE: &str = "playground";

/// The folder its files pretend to be in, which no message should show.
const SOURCE: &str = "/playground/src/";

/// The bundled library, every file of it, since there is no disk to find it on.
const CORE_FILES: [(&str, &str); 2] = [
    (
        "allocator.peep",
        include_str!("../../../../_builtin_library/core/src/allocator.peep"),
    ),
    (
        "global.peep",
        include_str!("../../../../_builtin_library/core/src/global.peep"),
    ),
];

/// The target a check assumes: what most programs are built for.
const TARGET: &str = "x86_64-linux";

fn main() -> ExitCode {
    let mut input = String::new();
    if let Err(error) = std::io::stdin().read_to_string(&mut input) {
        eprintln!("cannot read the files: {error}");
        return ExitCode::FAILURE;
    }
    match serde_json::from_str(&input) {
        Ok(files) => {
            println!("{}", check(files));
            ExitCode::SUCCESS
        }
        Err(error) => {
            eprintln!("the files are not a JSON object of texts: {error}");
            ExitCode::FAILURE
        }
    }
}

/// The diagnostics of the program `files` make up, as `peeper check` finds
/// them. `files` maps each file's path in the package to its text. Each entry
/// has its `severity`, `code` and full `message`, its primary `place` (null
/// when it has none) and the `related` places; a place has its `file`, its
/// `start` and `end`, and its own `message`.
fn check(files: HashMap<String, String>) -> Value {
    let source = Path::new(SOURCE);
    let core = Path::new("/core/src");
    let page_files = files
        .into_iter()
        .map(|(name, text)| (source.join(name), text));
    let core_files = CORE_FILES.map(|(name, text)| (core.join(name), text.to_string()));
    let open_files: HashMap<PathBuf, String> = page_files.chain(core_files).collect();
    let project = Project {
        packages: HashMap::from([
            (PACKAGE.to_string(), source.to_path_buf()),
            ("core".to_string(), core.to_path_buf()),
        ]),
        entry: source.join("main.peep"),
        root: PathBuf::from(format!("/{PACKAGE}")),
        name: PACKAGE.to_string(),
    };
    let program = project::load(&project, &open_files);
    // As in `peeper check`, errors in loading would only be repeated.
    let results = if program.checkable {
        let target = Machine::named(TARGET).expect("the target is a machine");
        compile::run(
            &program,
            target,
            compile::Results::default(),
            None,
            false,
            |_| {},
        )
    } else {
        compile::Results::default()
    };
    let place = |label: &Label| {
        let path = Path::new(&label.file.path);
        let name = path.strip_prefix(source).unwrap_or(path);
        json!({
            "file": name.to_string_lossy(),
            "start": position(label, label.span.start),
            "end": position(label, label.span.end),
            "message": label.message,
        })
    };
    let found = program.diagnostics.iter().chain(results.diagnostics());
    let entries = found.map(|diagnostic| {
        let severity = match diagnostic.code.severity {
            Severity::Error => "error",
            Severity::Warning => "warning",
            Severity::Info => "info",
            Severity::Hint => "hint",
        };
        let mut places = diagnostic.labels.iter().map(place);
        json!({
            "severity": severity,
            "code": diagnostic.code.id,
            "message": diagnostic.full_message().replace(SOURCE, ""),
            "place": places.next(),
            "related": places.collect::<Vec<_>>(),
        })
    });
    Value::Array(entries.collect())
}

/// The 1-based line and UTF-16 column of `offset` in `label`'s file.
fn position(label: &Label, offset: u32) -> Value {
    let (line, column) = label.file.utf16_position(offset as usize);
    json!({ "line": line, "column": column })
}

#[cfg(test)]
mod tests;
