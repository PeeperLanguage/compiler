//! Reads the source files of a program in parallel, resolves their imports,
//! and orders the modules so each comes after the modules it imports. It knows
//! packages only as names mapped to folders; finding them is the caller's job.

mod packages;

use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, mpsc};

use diagnostics::Diagnostic;
use model::syntax::Tree;
use source::{SourceFile, Span};

pub use packages::Packages;
use packages::{path_of, resolve};

/// Imported by every module except itself.
pub const PRELUDE_PATH: &str = "core:global";

/// One source file, read and parsed. It never changes once `load` has returned
/// it. Its source file is `syntax.file`.
pub struct Module {
    pub path: String, // import path, such as "myapp:utils/file" or "core:global"
    pub syntax: Tree,
    pub imports: Vec<Import>, // in source order; the implicit prelude import last
    pub diagnostics: Vec<Diagnostic>,
}

/// One resolved import of a module.
pub struct Import {
    pub path: String,          // import path of the imported module
    pub alias: Option<String>, // its name in the importing file; None for the implicit prelude import
    pub span: Span,            // where the import is written; empty for the implicit prelude import
}

/// Every module reachable from an entry file.
pub struct Program {
    pub modules: HashMap<String, Module>,
    pub order: Vec<String>, // each module after the modules it imports; outside cycles, the same order every run
    pub diagnostics: Vec<Diagnostic>, // about the program as a whole: import cycles
}

struct Job {
    path: String,
    file: PathBuf,
    standalone: bool, // a file in no package, checked on its own: it cannot import
}

/// Reads `entry` and every module it imports, directly or not. Files are read
/// and parsed by a pool of worker threads; only this thread touches the module
/// map, and each module is touched only by its worker until it is handed over.
/// An entry in no package's folder is checked on its own and cannot import.
pub fn load(packages: &Packages, entry: &Path) -> Program {
    let entry = std::path::absolute(entry).unwrap_or_else(|_| entry.to_path_buf());
    let with_prelude = resolve(packages, PRELUDE_PATH).is_ok_and(|(_, file)| file.is_file());
    let workers = std::thread::available_parallelism().map_or(1, |n| n.get());

    let mut modules = HashMap::new();
    let (send_job, jobs) = mpsc::channel::<Job>();
    let jobs = Mutex::new(jobs);
    let (send_module, done) = mpsc::channel();
    std::thread::scope(|scope| {
        for _ in 0..workers {
            let send_module = send_module.clone();
            let jobs = &jobs;
            scope.spawn(move || {
                loop {
                    // The lock is held only while taking a job.
                    let job = jobs
                        .lock()
                        .expect("a worker never panics holding the lock")
                        .recv();
                    let Ok(job) = job else { break }; // no more jobs
                    _ = send_module.send(load_file(packages, job, with_prelude));
                }
            });
        }

        let (path, in_package) = path_of(packages, &entry);
        let mut seen = HashSet::from([path.clone()]);
        _ = send_job.send(Job {
            path,
            file: entry,
            standalone: !in_package,
        });
        let mut pending = 1;
        while pending > 0 {
            let Ok(module) = done.recv() else { break };
            pending -= 1;
            for import in &module.imports {
                if seen.insert(import.path.clone()) {
                    let (_, file) =
                        resolve(packages, &import.path).expect("an import path is resolved");
                    _ = send_job.send(Job {
                        path: import.path.clone(),
                        file,
                        standalone: false,
                    });
                    pending += 1;
                }
            }
            modules.insert(module.path.clone(), module);
        }
        drop(send_job); // ends the workers
    });

    let (order, cycles) = sort_modules(&modules);
    Program {
        modules,
        order,
        diagnostics: cycles,
    }
}

/// Reads, parses and resolves the imports of one module. A file that cannot
/// be read, or is not UTF-8, gets an empty tree and one error.
fn load_file(packages: &Packages, job: Job, with_prelude: bool) -> Module {
    let name = job.file.to_string_lossy().into_owned();
    let (syntax, diagnostics) = match std::fs::read(&job.file) {
        Ok(bytes) => match String::from_utf8(bytes) {
            Ok(text) => parser::parse(&Arc::new(SourceFile::new(name, text))),
            Err(error) => {
                // The text up to the bad byte is kept as is, so the error points at it.
                let at = error.utf8_error().valid_up_to();
                let text = String::from_utf8_lossy(error.as_bytes()).into_owned();
                let file = Arc::new(SourceFile::new(name, text));
                let bad = Span::new(at, at + char::REPLACEMENT_CHARACTER.len_utf8());
                let error =
                    Diagnostic::new(diagnostics::UNEXPECTED_CHARACTER, "file is not valid UTF-8")
                        .label(&file, bad, "this byte is not UTF-8")
                        .note("Peeper source files must be UTF-8 text");
                (Tree::new(file), vec![error])
            }
        },
        Err(error) => {
            let message = format!("read module: {name}: {error}");
            let file = Arc::new(SourceFile::new(name, ""));
            let error = Diagnostic::new(diagnostics::MODULE_NOT_FOUND, message);
            (Tree::new(file), vec![error])
        }
    };
    let mut module = Module {
        path: job.path,
        syntax,
        imports: Vec::new(),
        diagnostics,
    };
    module.resolve_imports(packages, job.standalone, with_prelude);
    module
}

impl Module {
    fn resolve_imports(&mut self, packages: &Packages, standalone: bool, with_prelude: bool) {
        let file = self.syntax.file.clone();
        let mut aliases = HashMap::new(); // alias -> import path
        for import in &self.syntax.imports {
            let written =
                String::from_utf8_lossy(&literal::value(file.slice(import.path))).into_owned();
            let error = |code, message: String| {
                Diagnostic::new(code, message.clone()).label(&file, import.span, message)
            };
            if standalone {
                let message = "a single file cannot import; check its project folder instead";
                self.diagnostics
                    .push(error(diagnostics::INVALID_IMPORT_PATH, message.to_string()));
                continue;
            }
            let (path, imported_file) = match resolve(packages, &written) {
                Ok(resolved) => resolved,
                Err(message) => {
                    self.diagnostics
                        .push(error(diagnostics::INVALID_IMPORT_PATH, message));
                    continue;
                }
            };
            if !imported_file.is_file() {
                let message = format!("module not found: {}", imported_file.display());
                self.diagnostics
                    .push(error(diagnostics::MODULE_NOT_FOUND, message));
                continue;
            }
            let alias = match import.alias {
                Some(alias) => file.slice(alias).to_string(),
                None => path[path.rfind(['/', ':']).map_or(0, |i| i + 1)..].to_string(),
            };
            if let Some(previous) = aliases.get(&alias) {
                if *previous != path {
                    let message = "import alias already in use".to_string();
                    self.diagnostics
                        .push(error(diagnostics::ALIAS_IN_USE, message));
                }
                continue;
            }
            aliases.insert(alias.clone(), path.clone());
            if path == PRELUDE_PATH {
                self.diagnostics.push(
                    Diagnostic::new(
                        diagnostics::REDUNDANT_PRELUDE_IMPORT,
                        format!("`{written}` is imported automatically"),
                    )
                    .label(&file, import.span, "remove this import")
                    .note("global symbols are always in scope without an import"),
                );
            }
            self.imports.push(Import {
                path,
                alias: Some(alias),
                span: import.span,
            });
        }
        if with_prelude
            && self.path != PRELUDE_PATH
            && !self
                .imports
                .iter()
                .any(|import| import.path == PRELUDE_PATH)
        {
            self.imports.push(Import {
                path: PRELUDE_PATH.to_string(),
                alias: None,
                span: Span::at(0),
            });
        }
    }
}

#[derive(Clone, Copy, PartialEq)]
enum Visit {
    Started,
    Finished,
}

/// Orders modules so each follows the modules it imports, and reports every
/// import cycle. It visits modules and imports in path order, so the result
/// does not depend on how the workers were scheduled.
struct Sorter<'a> {
    modules: &'a HashMap<String, Module>,
    visits: HashMap<&'a str, Visit>,
    stack: Vec<&'a str>, // the modules being visited, outermost first
    order: Vec<String>,
    cycles: Vec<Diagnostic>,
}

fn sort_modules(modules: &HashMap<String, Module>) -> (Vec<String>, Vec<Diagnostic>) {
    let mut sorter = Sorter {
        modules,
        visits: HashMap::with_capacity(modules.len()),
        stack: Vec::new(),
        order: Vec::with_capacity(modules.len()),
        cycles: Vec::new(),
    };
    let mut paths: Vec<&str> = modules.keys().map(String::as_str).collect();
    paths.sort_unstable();
    for path in paths {
        if !sorter.visits.contains_key(path) {
            sorter.visit(path);
        }
    }
    (sorter.order, sorter.cycles)
}

impl<'a> Sorter<'a> {
    fn visit(&mut self, path: &'a str) {
        self.visits.insert(path, Visit::Started);
        self.stack.push(path);
        let module = &self.modules[path];
        let mut imports: Vec<&'a Import> = module.imports.iter().collect();
        imports.sort_by(|a, b| a.path.cmp(&b.path));
        for import in imports {
            match self.visits.get(import.path.as_str()) {
                None => self.visit(&import.path),
                Some(Visit::Started) => {
                    let start = self
                        .stack
                        .iter()
                        .position(|&p| p == import.path)
                        .unwrap_or(0);
                    let mut cycle = self.stack[start..].to_vec();
                    cycle.push(&import.path);
                    let d = Diagnostic::new(
                        diagnostics::CYCLIC_IMPORT,
                        format!("cyclic import detected: {}", cycle.join(" -> ")),
                    );
                    self.cycles.push(match import.alias {
                        None => d.note(format!("every module imports `{PRELUDE_PATH}` implicitly")),
                        Some(_) => d.label(
                            &module.syntax.file,
                            import.span,
                            "this import closes the cycle",
                        ),
                    });
                }
                Some(Visit::Finished) => {}
            }
        }
        self.stack.pop();
        self.visits.insert(path, Visit::Finished);
        self.order.push(path.to_string());
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Writes `files` (paths relative to a new temporary folder) and returns that
    /// folder and its packages: "myapp" in src, "core" in libs/core/src.
    fn write(test: &str, files: &[(&str, &str)]) -> (PathBuf, Packages) {
        let root =
            std::env::temp_dir().join(format!("peeper-project-{}-{test}", std::process::id()));
        _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).unwrap();
        for (name, text) in files {
            let file = root.join(name);
            std::fs::create_dir_all(file.parent().unwrap()).unwrap();
            std::fs::write(file, text).unwrap();
        }
        let packages = Packages::from([
            ("myapp".to_string(), root.join("src")),
            ("core".to_string(), root.join("libs/core/src")),
        ]);
        (root, packages)
    }

    /// "module: CODE message" for every diagnostic, sorted.
    fn messages(program: &Program) -> Vec<String> {
        let mut out: Vec<String> = program
            .diagnostics
            .iter()
            .map(|d| ("program", d))
            .chain(
                program
                    .modules
                    .values()
                    .flat_map(|m| m.diagnostics.iter().map(|d| (m.path.as_str(), d))),
            )
            .map(|(module, d)| format!("{module}: {} {}", d.code.id, d.message))
            .collect();
        out.sort();
        out
    }

    fn imports(module: &Module) -> Vec<String> {
        let alias = |import: &Import| import.alias.clone().unwrap_or_default();
        module
            .imports
            .iter()
            .map(|i| format!("{}={}", alias(i), i.path))
            .collect()
    }

    #[test]
    fn loads_nested_imports() {
        let (root, packages) = write(
            "nested",
            &[
                (
                    "src/main.peep",
                    "import \"myapp:a\";\nimport \"myapp:b/c\" as c2;\nfn main() {}",
                ),
                ("src/a.peep", "import \"myapp:b/c\";\nfn A() {}"),
                ("src/b/c.peep", "fn C() {}"),
                ("libs/core/src/global.peep", "fn exit(code: i32) {}"),
            ],
        );
        let program = load(&packages, &root.join("src/main.peep"));
        assert_eq!(messages(&program), Vec::<String>::new());
        assert_eq!(
            program.order.join(" "),
            "core:global myapp:b/c myapp:a myapp:main"
        );
        assert_eq!(
            imports(&program.modules["myapp:main"]),
            ["a=myapp:a", "c2=myapp:b/c", "=core:global"]
        );
        assert!(
            program.modules["core:global"].imports.is_empty(),
            "the prelude imports itself"
        );
    }

    #[test]
    fn import_errors() {
        let main = [
            r#"import "myapp:missing";"#,
            r#"import "myapp:./x";"#,
            r#"import "myapp:../x";"#,
            r#"import "myapp:";"#,
            r#"import "other:x";"#,
            r#"import "x/y";"#,
            r#"import "myapp:x.txt";"#,
            r#"import "myapp:a";"#,
            r#"import "myapp:c" as a;"#,
            r#"import "myapp:a.peep";"#, // the same module as myapp:a under the same name: kept once
            r#"import "myapp:b";"#,
        ]
        .join("\n");
        let (root, packages) = write(
            "errors",
            &[
                ("src/main.peep", &main),
                ("src/a.peep", ""),
                ("src/b.peep", "fn f( {"), // a syntax error belongs to b's own diagnostics
                ("src/c.peep", ""),
            ],
        );
        let program = load(&packages, &root.join("src/main.peep"));
        let missing = root.join("src/missing.peep");
        let want = [
            format!("myapp:main: M0001 module not found: {}", missing.display()),
            "myapp:main: M0003 import path needs a package, such as `myapp:utils/file`".to_string(),
            "myapp:main: M0003 import path parts cannot be empty, `.` or `..`".to_string(),
            "myapp:main: M0003 import path parts cannot be empty, `.` or `..`".to_string(),
            "myapp:main: M0003 import path parts cannot be empty, `.` or `..`".to_string(),
            r#"myapp:main: M0003 invalid import extension ".txt""#.to_string(),
            "myapp:main: M0003 unknown package `other`".to_string(),
            "myapp:main: M0005 import alias already in use".to_string(),
        ];
        let got: Vec<String> = messages(&program)
            .into_iter()
            .filter(|m| m.starts_with("myapp:main: "))
            .collect();
        assert_eq!(got, want);
        let b = &program.modules["myapp:b"];
        assert_eq!(b.diagnostics[0].code, diagnostics::EXPECTED_TOKEN);
    }

    #[test]
    fn reports_imports_at_their_line() {
        let (root, packages) = write(
            "line",
            &[("src/main.peep", "fn main() {}\nimport \"myapp:missing\";\n")],
        );
        let program = load(&packages, &root.join("src/main.peep"));
        let main = &program.modules["myapp:main"];
        assert_eq!(main.diagnostics.len(), 1, "{:?}", messages(&program));
        let label = &main.diagnostics[0].labels[0];
        assert_eq!(main.syntax.file.position(label.span.start as usize), (2, 1));
    }

    #[test]
    fn reports_a_cycle_at_the_import_closing_it() {
        let (root, packages) = write(
            "cycle",
            &[
                ("src/main.peep", r#"import "myapp:a";"#),
                ("src/a.peep", r#"import "myapp:b";"#),
                ("src/b.peep", r#"import "myapp:a";"#),
            ],
        );
        let program = load(&packages, &root.join("src/main.peep"));
        assert_eq!(
            messages(&program),
            ["program: M0002 cyclic import detected: myapp:a -> myapp:b -> myapp:a"]
        );
        let label = &program.diagnostics[0].labels[0];
        assert!(Arc::ptr_eq(
            &label.file,
            &program.modules["myapp:b"].syntax.file
        ));
    }

    #[test]
    fn redundant_prelude_import() {
        let (root, packages) = write(
            "prelude",
            &[
                ("src/main.peep", r#"import "core:global";"#),
                ("libs/core/src/global.peep", ""),
            ],
        );
        let program = load(&packages, &root.join("src/main.peep"));
        assert_eq!(
            messages(&program),
            ["myapp:main: S0004 `core:global` is imported automatically"]
        );
        // the implicit import must not repeat an explicit one
        assert_eq!(
            imports(&program.modules["myapp:main"]),
            ["global=core:global"]
        );
    }

    #[test]
    fn a_single_file_cannot_import() {
        let (root, packages) = write(
            "single",
            &[
                ("tool.peep", "import \"myapp:a\";\nfn main() {}"),
                ("libs/core/src/global.peep", ""),
            ],
        );
        let program = load(&packages, &root.join("tool.peep"));
        assert_eq!(
            messages(&program),
            ["tool: M0003 a single file cannot import; check its project folder instead"]
        );
        assert_eq!(
            imports(&program.modules["tool"]),
            ["=core:global"],
            "the prelude still comes in"
        );
    }

    #[test]
    fn unreadable_and_non_utf8_entries() {
        let (root, packages) = write("unreadable", &[("src/bad.peep", "fn \u{0} main")]);
        std::fs::write(root.join("src/bad.peep"), b"fn \xff main() {}").unwrap();
        let program = load(&packages, &root.join("src/main.peep"));
        let got = messages(&program);
        assert!(
            got.len() == 1 && got[0].starts_with("myapp:main: M0001 read module: "),
            "{got:?}"
        );
        let program = load(&packages, &root.join("src/bad.peep"));
        assert_eq!(
            messages(&program),
            ["myapp:bad: L0001 file is not valid UTF-8"]
        );
    }

    /// A wide, deep project; the order must be the same every run.
    #[test]
    fn loads_many_modules_in_a_stable_order() {
        let mut files = vec![("libs/core/src/global.peep".to_string(), String::new())];
        let mut main = String::new();
        for i in 0..200 {
            main.push_str(&format!("import \"myapp:m{i}\";\n"));
            let mut module = String::new();
            for j in i + 1..(i + 4).min(200) {
                module.push_str(&format!("import \"myapp:m{j}\";\n"));
            }
            module.push_str("fn f() {}\n");
            files.push((format!("src/m{i}.peep"), module));
        }
        files.push(("src/main.peep".to_string(), main));
        let files: Vec<(&str, &str)> = files
            .iter()
            .map(|(n, t)| (n.as_str(), t.as_str()))
            .collect();
        let (root, packages) = write("many", &files);
        let entry = root.join("src/main.peep");
        let first = load(&packages, &entry);
        assert!(messages(&first).is_empty());
        assert_eq!(first.modules.len(), 202);
        let position: HashMap<&str, usize> = first
            .order
            .iter()
            .enumerate()
            .map(|(i, p)| (p.as_str(), i))
            .collect();
        for (path, module) in &first.modules {
            for import in &module.imports {
                assert!(
                    position[import.path.as_str()] < position[path.as_str()],
                    "{path} before {}",
                    import.path
                );
            }
        }
        for _ in 0..5 {
            assert_eq!(
                load(&packages, &entry).order,
                first.order,
                "order differs between runs"
            );
        }
    }

    #[test]
    fn path_of_a_file() {
        let (root, packages) = write("path", &[]);
        assert_eq!(
            path_of(&packages, &root.join("src/deep/x.peep")),
            ("myapp:deep/x".to_string(), true)
        );
        assert_eq!(
            path_of(&packages, &root.join("tool.peep")),
            ("tool".to_string(), false)
        );
        assert_eq!(
            resolve(&packages, "myapp:a.b.peep"),
            Ok(("myapp:a.b".to_string(), root.join("src/a.b.peep")))
        );
    }
}
