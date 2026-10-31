//! The checker a web page runs, built for `wasm32-wasip1`: reads a request from
//! stdin as a JSON object, with `files`, an object from file name to text, and
//! `run`, true to run the program as well. It writes the program's diagnostics
//! to stdout as a JSON list, or for a run an object (`run`). The program starts
//! at main.peep, and a file imports another as "playground:name". Nothing is read
//! from a disk. The compiler can overflow wasm's stack on very deep code, so a
//! page runs a new instance for each check, and one crash loses nothing.

use std::collections::HashMap;
use std::io::Read;
use std::path::Path;
use std::path::PathBuf;
use std::process::ExitCode;

use diagnostics::Label;
use diagnostics::Severity;
use model::ModuleId;
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
    let request = serde_json::from_str::<Value>(&input).and_then(|mut request| {
        let files = request.get_mut("files").map(Value::take);
        let files = serde_json::from_value(files.unwrap_or_default())?;
        Ok((files, request["run"] == true))
    });
    match request {
        Ok((files, false)) => println!("{}", check(files)),
        Ok((files, true)) => println!("{}", run(files)),
        Err(error) => {
            eprintln!("the request is not an object with `files` of texts: {error}");
            return ExitCode::FAILURE;
        }
    }
    ExitCode::SUCCESS
}

/// The program `files` make up: `files` maps each file's path in the package
/// to its text.
fn load(files: HashMap<String, String>) -> (Project, project::Program) {
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
    (project, program)
}

/// The diagnostics of the program `files` make up, as `peeper check` finds
/// them (`entries`). `files` maps each file's path in the package to its text.
fn check(files: HashMap<String, String>) -> Value {
    let (_, program) = load(files);
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
    entries(&program, &results)
}

/// Compiles the program `files` make up and runs it with no backend. Gives its
/// diagnostics as `entries`, and `run`, null unless they let it start, with
/// what it printed to `stdout` and `stderr` (a panic's text too) and its
/// `exit_code`.
fn run(files: HashMap<String, String>) -> Value {
    let (project, program) = load(files);
    let target = Machine::named(TARGET).expect("the target is a machine");
    let (results, ran) = interpret(&project, &program, target);
    let ran = ran.map(|ran| {
        json!({
            "stdout": String::from_utf8_lossy(&ran.stdout),
            "stderr": String::from_utf8_lossy(&ran.stderr),
            "exit_code": ran.exit_code,
        })
    });
    json!({ "entries": entries(&program, &results), "run": ran })
}

/// Compiles and runs `program` for `target`. A program that cannot be checked
/// has only loading's errors, and does not start.
fn interpret(
    project: &Project,
    program: &project::Program,
    target: &'static Machine,
) -> (compile::Results, Option<compile::Interpreted>) {
    let is_entry = |module: &project::Module| Path::new(&module.ast.file.path) == project.entry;
    match program.modules.iter().position(is_entry) {
        Some(entry) if program.checkable => {
            compile::interpret(program, target, ModuleId::from_index(entry))
        }
        _ => (compile::Results::default(), None),
    }
}

/// Each diagnostic of `program` and `results`: its `severity`, `code` and full
/// `message`, its primary `place` (null when it has none) and the `related`
/// places; a place has its `file`, its `start` and `end`, and its own
/// `message`.
fn entries(program: &project::Program, results: &compile::Results) -> Value {
    let source = Path::new(SOURCE);
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
