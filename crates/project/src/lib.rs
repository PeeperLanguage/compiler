//! Reads the source files of a program in parallel, resolves their imports,
//! and orders the modules so each comes after the modules it imports. `find`
//! gives the packages and entry file of a project folder or a single file.

pub mod manifest;
mod packages;

use std::collections::HashMap;
use std::collections::HashSet;
use std::ffi::OsString;
use std::io::ErrorKind;
use std::io::Write;
use std::path::Component;
use std::path::Path;
use std::path::PathBuf;
use std::path::Prefix;
use std::sync::Arc;
use std::sync::Mutex;
use std::sync::mpsc;

use diagnostics::Diagnostic;
use model::ModuleId;
use model::ast;
use model::ast::Tree;
use source::SourceFile;
use source::Span;

use packages::SOURCE_EXTENSION;
use packages::path_of;
use packages::resolve;

/// Imported by every module except itself.
const PRELUDE_PATH: &str = "core:global";
/// The folder of a package that holds its source files.
const SOURCE_FOLDER: &str = "src";

/// One source file, read and parsed. Its source file is `ast.file`. What
/// `load` read never changes; what a compile finds, its scope included, is
/// kept by the compile (`check::Queries`).
pub struct Module {
    pub path: String, // import path, such as "myapp:utils/file" or "core:global"
    pub ast: Tree,
    pub imports: Vec<Import>, // in source order; the implicit prelude import last, in every module but the prelude
}

/// One resolved import of a module.
pub struct Import {
    pub kind: ImportKind,
    pub module: ModuleId, // the imported module
}

/// Where an import comes from.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ImportKind {
    /// The importer's `ast.imports[i]`, written in its file.
    Declared(u32),
    /// The implicit import of `core:global`.
    Prelude,
}

impl ImportKind {
    /// The import as written in `importer`; `None` for the prelude's.
    pub fn declaration(self, importer: &Tree) -> Option<&ast::Import> {
        match self {
            ImportKind::Declared(index) => Some(&importer.imports[index as usize]),
            ImportKind::Prelude => None,
        }
    }
}

impl Import {
    /// The name the import brings into `importer`, and where it is written:
    /// its alias, else the last segment of the imported module's path (in
    /// `modules`), at the import. `None` for the prelude, whose names are
    /// looked up, not brought in.
    pub fn name<'a>(&self, importer: &'a Module, modules: &'a [Module]) -> Option<(&'a str, Span)> {
        let declaration = self.kind.declaration(&importer.ast)?;
        let name = match declaration.alias {
            Some(alias) => alias.text(&importer.ast.file),
            None => default_alias(&modules[self.module.index()].path),
        };
        Some((name, declaration.span))
    }
}

/// The name an import without `as` gets: the last segment of the imported
/// module's path, such as "file" for "myapp:utils/file".
fn default_alias(path: &str) -> &str {
    &path[path.rfind(['/', ':']).map_or(0, |i| i + 1)..]
}

/// Every module reachable from an entry file.
pub struct Program {
    /// Each module after the modules it imports; outside cycles, the same
    /// order every run.
    pub modules: Vec<Module>,
    /// What loading found: import cycles first, then each module's errors,
    /// in the program's order. One list for the whole program, because every
    /// load reads every module again; what a compile finds is kept per
    /// module instead (`compile::Results`), so an unchanged module can reuse it.
    pub diagnostics: Vec<Diagnostic>,
    /// Whether the program can be checked: every module's text is all in its
    /// tree, every import brings in its module, and no import cycle was found.
    /// Checking with a hole in a tree or a missing import would only add
    /// reports of it. A syntax error the parser read past, such as a missing
    /// `;` at a line end, leaves no hole.
    pub checkable: bool,
}

impl Program {
    /// The module read from `file`.
    pub fn module_of(&self, file: &Path) -> Option<&Module> {
        self.modules
            .iter()
            .find(|module| Path::new(&module.ast.file.path) == file)
    }
}

/// What `load` needs to check a project folder or a single file.
pub struct Project {
    /// Each package's name mapped to the folder holding its source files: the
    /// bundled libraries, and the project's own. For example "myapp" to
    /// <project>/src and "core" to <installation>/libs/core/src; an import
    /// "myapp:utils/file" names <project>/src/utils/file.peep.
    pub packages: HashMap<String, PathBuf>,
    pub entry: PathBuf, // the file the program starts at
    pub root: PathBuf,  // the project folder, or the folder of the file
    pub name: String,   // the project's package name, or the file's name without `.peep`
}

/// The one spelling of `path` that files are found and compared by:
/// absolute, without `.` and `..` parts, and on Windows with an upper case
/// drive letter and no `\\?\` prefix. `..` is read as text, as Go's
/// `filepath.Clean` does: symbolic links are not followed. Every path from
/// outside goes through it: the command line's, the libraries folder's and
/// each file an editor names.
pub fn canonical_path(path: &Path) -> std::io::Result<PathBuf> {
    let mut canonical = PathBuf::new();
    for part in std::path::absolute(path)?.components() {
        match part {
            Component::Prefix(prefix) => canonical.push(match prefix.kind() {
                Prefix::Disk(letter) | Prefix::VerbatimDisk(letter) => {
                    OsString::from(format!("{}:", letter.to_ascii_uppercase() as char))
                }
                Prefix::UNC(server, share) | Prefix::VerbatimUNC(server, share) => {
                    let mut unc = OsString::from(r"\\");
                    unc.push(server);
                    unc.push(r"\");
                    unc.push(share);
                    unc
                }
                _ => prefix.as_os_str().to_os_string(),
            }),
            Component::CurDir => {}
            Component::ParentDir => _ = canonical.pop(), // never past the root
            part => canonical.push(part),
        }
    }
    Ok(canonical)
}

/// The project at `path`: a folder holding peeper.toml, whose program starts
/// at src/main.peep, or a single file, checked on its own with no imports.
/// `libraries` holds the bundled libraries, one folder each. The error is a
/// full message naming the file it is about.
pub fn find(path: &Path, libraries: &Path) -> Result<Project, String> {
    let path = canonical_path(path).map_err(|error| format!("{}: {error}", path.display()))?;
    let is_dir = std::fs::metadata(&path)
        .map_err(|error| format!("{}: {error}", path.display()))?
        .is_dir();
    let mut packages = bundled_libraries(libraries);
    if !is_dir {
        let root = path.parent().unwrap_or(Path::new("")).to_path_buf();
        let name = path
            .file_stem()
            .unwrap_or_default()
            .to_string_lossy()
            .into_owned();
        return Ok(Project {
            packages,
            entry: path,
            root,
            name,
        });
    }
    let manifest = manifest::read(&path)?;
    if packages.contains_key(&manifest.name) {
        let file = path.join(manifest::FILE_NAME);
        return Err(format!(
            "{}: project name `{}` is taken by a bundled library",
            file.display(),
            manifest.name
        ));
    }
    let source = path.join(SOURCE_FOLDER);
    let entry = entry(&source);
    packages.insert(manifest.name.clone(), source);
    Ok(Project {
        packages,
        entry,
        root: path,
        name: manifest.name,
    })
}

/// The entry file of the package whose source folder is `source`.
fn entry(source: &Path) -> PathBuf {
    source.join(format!("main{SOURCE_EXTENSION}"))
}

/// The entry file a new project starts with.
const HELLO: &str = "fn main() {\n    write(stdout, c\"Hello from Peeper!\\n\", 19);\n}\n";

/// Makes the folder `dir` the project `name`: writes its peeper.toml, and its
/// src/main.peep unless that file is there already. Returns whether it wrote
/// src/main.peep. The error is a full message naming the file it is about;
/// an existing peeper.toml is one, and is left as it was, and so is a name
/// that is not a package name or is taken by a library in `libraries`.
pub fn create(dir: &Path, name: &str, libraries: &Path) -> Result<bool, String> {
    let manifest = dir.join(manifest::FILE_NAME);
    if !manifest::is_valid_name(name) {
        return Err(format!("`{name}`: {}", manifest::NAME_RULE));
    }
    if bundled_libraries(libraries).contains_key(name) {
        return Err(format!(
            "project name `{name}` is taken by a bundled library"
        ));
    }
    let write_new = |file: &Path, text: &str| {
        std::fs::File::create_new(file).and_then(|mut file| file.write_all(text.as_bytes()))
    };
    write_new(&manifest, &format!("name = \"{name}\"\n")).map_err(|error| match error.kind() {
        ErrorKind::AlreadyExists => format!("{}: already exists", manifest.display()),
        _ => format!("{}: {error}", manifest.display()),
    })?;
    let source = dir.join(SOURCE_FOLDER);
    std::fs::create_dir_all(&source).map_err(|error| format!("{}: {error}", source.display()))?;
    let main = entry(&source);
    match write_new(&main, HELLO) {
        Ok(()) => Ok(true),
        Err(error) if error.kind() == ErrorKind::AlreadyExists => Ok(false),
        Err(error) => Err(format!("{}: {error}", main.display())),
    }
}

/// Maps each library folder in `libraries` to its src folder. No libraries
/// folder means no bundled libraries.
fn bundled_libraries(libraries: &Path) -> HashMap<String, PathBuf> {
    let Ok(libraries) = canonical_path(libraries) else {
        return HashMap::new(); // an empty path names no folder
    };
    let Ok(entries) = std::fs::read_dir(&libraries) else {
        return HashMap::new();
    };
    entries
        .flatten()
        .filter(|entry| entry.path().is_dir())
        .filter_map(|entry| {
            let name = entry.file_name().into_string().ok()?;
            manifest::is_valid_name(&name).then(|| (name, entry.path().join(SOURCE_FOLDER)))
        })
        .collect()
}

/// A module being loaded: its imports are known by path until every module
/// has its place in the program's order.
struct Loaded {
    path: String,
    ast: Tree,
    imports: Vec<(ImportKind, String)>, // with the imported module's path
    imports_complete: bool,             // every written import names a module it brings in
    diagnostics: Vec<Diagnostic>,
}

struct Job {
    path: String,
    file: PathBuf,
    standalone: bool, // a file in no package, checked on its own: it cannot import
}

/// Reads `project`'s entry and every module it imports, directly or not. Files
/// are read and parsed by a pool of worker threads; only this thread touches
/// the module map, and each module is touched only by its worker until it is
/// handed over.
/// An entry in no package's folder is checked on its own and cannot import.
///
/// `open_files` holds the text of files open in an editor (anything that
/// reads as text, such as a `String`), read instead of what is on disk. Its
/// keys, like `project`'s paths, are spelled as `canonical_path` spells them.
/// Each `.peep` file of a package in it is loaded too, imported or not, so an
/// editor checks the file it has open.
pub fn load(project: &Project, open_files: &HashMap<PathBuf, impl AsRef<str> + Sync>) -> Program {
    let with_prelude = resolve(&project.packages, PRELUDE_PATH)
        .is_ok_and(|(_, file)| file.is_file() || open_files.contains_key(&file));
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
                    let loaded = load_file(&project.packages, open_files, job, with_prelude);
                    _ = send_module.send(loaded);
                }
            });
        }

        let (path, in_package) = path_of(&project.packages, &project.entry);
        let mut seen = HashSet::from([path.clone()]);
        _ = send_job.send(Job {
            path,
            file: project.entry.clone(),
            standalone: !in_package,
        });
        let mut pending = 1;
        // A module of the project that an editor has open is checked even
        // when no import reaches it from the entry.
        for file in open_files
            .keys()
            .filter(|file| file.to_string_lossy().ends_with(SOURCE_EXTENSION))
        {
            let (path, in_package) = path_of(&project.packages, file);
            if in_package && seen.insert(path.clone()) {
                _ = send_job.send(Job {
                    path,
                    file: file.clone(),
                    standalone: false,
                });
                pending += 1;
            }
        }
        while pending > 0 {
            let Ok(loaded) = done.recv() else { break };
            pending -= 1;
            for (_, path) in &loaded.imports {
                if seen.insert(path.clone()) {
                    let (_, file) =
                        resolve(&project.packages, path).expect("an import path is resolved");
                    _ = send_job.send(Job {
                        path: path.clone(),
                        file,
                        standalone: false,
                    });
                    pending += 1;
                }
            }
            modules.insert(loaded.path.clone(), loaded);
        }
        drop(send_job); // ends the workers
    });

    let (order, cycles) = sort_modules(&modules);
    // The order holds every module: the sort visits each.
    let ids: HashMap<String, ModuleId> = order
        .iter()
        .cloned()
        .enumerate()
        .map(|(index, path)| (path, ModuleId::from_index(index)))
        .collect();
    let mut diagnostics = cycles;
    let mut checkable = !diagnostics.iter().any(Diagnostic::is_error);
    let modules = order
        .iter()
        .filter_map(|path| modules.remove(path))
        .map(|mut loaded| {
            checkable &= loaded.ast.complete && loaded.imports_complete;
            diagnostics.append(&mut loaded.diagnostics);
            loaded.into_module(&ids)
        })
        .collect();
    Program {
        modules,
        diagnostics,
        checkable,
    }
}

/// Reads, parses and resolves the imports of one module, taking its text from
/// `open_files` when it is open there. A file that cannot be read, or is not
/// UTF-8, gets an empty tree and one error.
fn load_file(
    packages: &HashMap<String, PathBuf>,
    open_files: &HashMap<PathBuf, impl AsRef<str>>,
    job: Job,
    with_prelude: bool,
) -> Loaded {
    let path = job.file.to_string_lossy().into_owned();
    let text = match open_files.get(&job.file) {
        // Copied: every worker shares the map, and a source file owns its text.
        Some(text) => Ok(Ok(text.as_ref().to_string())),
        None => std::fs::read(&job.file).map(String::from_utf8),
    };
    let (ast, diagnostics) = match text {
        Ok(Ok(text)) => parser::parse(&Arc::new(SourceFile::new(path, text))),
        Ok(Err(error)) => {
            // The text up to the bad byte is kept as is, so the error points at it.
            let at = error.utf8_error().valid_up_to();
            let text = String::from_utf8_lossy(error.as_bytes()).into_owned();
            let file = Arc::new(SourceFile::new(path, text));
            let bad = Span::new(at, at + char::REPLACEMENT_CHARACTER.len_utf8());
            let error =
                Diagnostic::new(diagnostics::UNEXPECTED_CHARACTER, "file is not valid UTF-8")
                    .label(&file, bad, "this byte is not UTF-8")
                    .note("Peeper source files must be UTF-8 text");
            (Tree::new(file), vec![error])
        }
        Err(error) => {
            let message = format!("read module: {path}: {error}");
            let file = Arc::new(SourceFile::new(path, ""));
            let error = Diagnostic::new(diagnostics::MODULE_NOT_FOUND, message);
            (Tree::new(file), vec![error])
        }
    };
    let mut loaded = Loaded {
        path: job.path,
        ast,
        imports: Vec::new(),
        imports_complete: true,
        diagnostics,
    };
    loaded.resolve_imports(packages, open_files, job.standalone, with_prelude);
    loaded
}

impl Loaded {
    /// The module, its imports resolved to the places in `ids`.
    fn into_module(self, ids: &HashMap<String, ModuleId>) -> Module {
        let imports = self.imports.iter();
        let imports = imports.map(|(kind, path)| Import {
            kind: *kind,
            module: ids[path], // every imported module is loaded
        });
        Module {
            path: self.path,
            ast: self.ast,
            imports: imports.collect(),
        }
    }

    fn resolve_imports(
        &mut self,
        packages: &HashMap<String, PathBuf>,
        open_files: &HashMap<PathBuf, impl AsRef<str>>,
        standalone: bool,
        with_prelude: bool,
    ) {
        let file = self.ast.file.clone();
        let mut aliases = HashMap::new(); // alias -> import path
        for (index, import) in (0..).zip(&self.ast.imports) {
            let written =
                String::from_utf8_lossy(&literal::value(file.slice(import.path))).into_owned();
            let error = |code, message: String| {
                Diagnostic::new(code, message).label(&file, import.span, "")
            };
            if standalone {
                let message = "a single file cannot import; check its project folder instead";
                self.diagnostics
                    .push(error(diagnostics::INVALID_IMPORT_PATH, message.to_string()));
                self.imports_complete = false;
                continue;
            }
            let (path, imported_file) = match resolve(packages, &written) {
                Ok(resolved) => resolved,
                Err(message) => {
                    self.diagnostics
                        .push(error(diagnostics::INVALID_IMPORT_PATH, message));
                    self.imports_complete = false;
                    continue;
                }
            };
            if !imported_file.is_file() && !open_files.contains_key(&imported_file) {
                let message = format!("module not found: {}", imported_file.display());
                self.diagnostics
                    .push(error(diagnostics::MODULE_NOT_FOUND, message));
                self.imports_complete = false;
                continue;
            }
            let alias = match import.alias {
                Some(alias) => alias.text(&file).to_string(),
                None => default_alias(&path).to_string(),
            };
            if let Some(previous) = aliases.get(&alias) {
                if *previous != path {
                    let message = "import alias already in use".to_string();
                    self.diagnostics
                        .push(error(diagnostics::ALIAS_IN_USE, message));
                    self.imports_complete = false;
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
            self.imports.push((ImportKind::Declared(index), path));
        }
        // Even next to a written import of it, so every module but the
        // prelude finds its prelude among its imports.
        if with_prelude && self.path != PRELUDE_PATH {
            self.imports
                .push((ImportKind::Prelude, PRELUDE_PATH.to_string()));
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
    modules: &'a HashMap<String, Loaded>,
    visits: HashMap<&'a str, Visit>,
    stack: Vec<&'a str>, // the modules being visited, outermost first
    order: Vec<String>,
    cycles: Vec<Diagnostic>,
}

fn sort_modules(modules: &HashMap<String, Loaded>) -> (Vec<String>, Vec<Diagnostic>) {
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
        let mut imports: Vec<&'a (ImportKind, String)> = module.imports.iter().collect();
        imports.sort_by(|(_, a), (_, b)| a.cmp(b));
        // A module imported twice (`core:global`, written and implicit) is
        // one edge; the stable sort keeps the written import, which has a
        // place to point at.
        imports.dedup_by(|(_, a), (_, b)| a == b);
        for (kind, imported) in imports {
            match self.visits.get(imported.as_str()) {
                None => self.visit(imported),
                Some(Visit::Started) => {
                    let start = self.stack.iter().position(|&p| p == imported).unwrap_or(0);
                    let mut cycle = self.stack[start..].to_vec();
                    cycle.push(imported);
                    let diagnostic = Diagnostic::new(
                        diagnostics::CYCLIC_IMPORT,
                        format!("cyclic import detected: {}", cycle.join(" -> ")),
                    );
                    self.cycles.push(match kind.declaration(&module.ast) {
                        None => diagnostic
                            .note(format!("every module imports `{PRELUDE_PATH}` implicitly")),
                        Some(declaration) => diagnostic.label(
                            &module.ast.file,
                            declaration.span,
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

    /// The module of `program` at the import path `path`.
    fn module_named<'p>(program: &'p Program, path: &str) -> &'p Module {
        let module = program.modules.iter().find(|module| module.path == path);
        module.unwrap_or_else(|| panic!("no module {path}"))
    }

    /// The paths of `program`'s modules, in its order.
    fn order(program: &Program) -> Vec<&str> {
        program
            .modules
            .iter()
            .map(|module| module.path.as_str())
            .collect()
    }

    /// Writes `files` (paths relative to a new temporary folder) and returns that
    /// folder and its packages: "myapp" in src, "core" in libs/core/src.
    fn write(test: &str, files: &[(&str, &str)]) -> Project {
        let root =
            std::env::temp_dir().join(format!("peeper-project-{}-{test}", std::process::id()));
        _ = std::fs::remove_dir_all(&root);
        std::fs::create_dir_all(&root).unwrap();
        for (name, text) in files {
            let file = root.join(name);
            std::fs::create_dir_all(file.parent().unwrap()).unwrap();
            std::fs::write(file, text).unwrap();
        }
        let packages = HashMap::from([
            ("myapp".to_string(), root.join("src")),
            ("core".to_string(), root.join("libs/core/src")),
        ]);
        Project {
            packages,
            entry: root.join("src/main.peep"),
            root,
            name: "myapp".to_string(),
        }
    }

    /// "module: CODE message" for every diagnostic, sorted.
    /// Each diagnostic as the path of the module its first label is in, its
    /// code and its message, sorted.
    fn messages(program: &Program) -> Vec<String> {
        let module = |d: &Diagnostic| {
            let file = Path::new(&d.labels.first()?.file.path);
            Some(program.module_of(file)?.path.as_str())
        };
        let mut out: Vec<String> = program
            .diagnostics
            .iter()
            .map(|d| {
                format!(
                    "{}: {} {}",
                    module(d).unwrap_or("program"),
                    d.code.id,
                    d.message
                )
            })
            .collect();
        out.sort();
        out
    }

    fn imports(program: &Program, module: &Module) -> Vec<String> {
        let alias = |import: &Import| {
            let name = import.name(module, &program.modules);
            name.map_or("", |(name, _)| name)
        };
        let path = |import: &Import| &program.modules[import.module.index()].path;
        module
            .imports
            .iter()
            .map(|i| format!("{}={}", alias(i), path(i)))
            .collect()
    }

    /// `..` and `.` are dropped as text, and nothing goes above the root.
    #[test]
    fn canonical_paths() {
        let here = std::env::current_dir().unwrap();
        for (path, want) in [
            ("/a/./b/../c", PathBuf::from("/a/c")),
            ("/a/b/../../..", PathBuf::from("/")),
            ("x/../y", here.join("y")),
        ] {
            assert_eq!(canonical_path(Path::new(path)).unwrap(), want, "{path}");
        }
    }

    /// On Windows a drive letter is upper case and a `\\?\` prefix is dropped,
    /// as an editor and `std::fs::canonicalize` spell the same file apart.
    #[cfg(windows)]
    #[test]
    fn canonical_windows_paths() {
        for (path, want) in [
            (r"c:\Work Dir\main.peep", r"C:\Work Dir\main.peep"),
            (r"\\?\C:\a\b", r"C:\a\b"),
            (r"C:/a/./b", r"C:\a\b"),
            (r"\\?\UNC\server\share\x", r"\\server\share\x"),
        ] {
            assert_eq!(
                canonical_path(Path::new(path)).unwrap(),
                PathBuf::from(want),
                "{path}"
            );
        }
    }

    /// A file named from the current folder gets an absolute root, the form
    /// `load` gives its files' paths.
    #[test]
    fn a_single_file_has_an_absolute_root() {
        let project = find(Path::new("src/lib.rs"), Path::new("")).unwrap();
        let here = std::env::current_dir().unwrap();
        assert_eq!(project.root, here.join("src"));
        assert_eq!(project.entry, here.join("src/lib.rs"));
    }

    #[test]
    fn create_makes_a_project_find_reads() {
        let root = write("create", &[("libs/core/src/global.peep", "")]).root;
        let create = |name| create(&root, name, &root.join("libs"));
        assert_eq!(
            create("my-app").map_err(|e| e.contains(manifest::NAME_RULE)),
            Err(true)
        );
        let taken = "project name `core` is taken by a bundled library";
        assert_eq!(create("core"), Err(taken.to_string()));
        assert!(!root.join(manifest::FILE_NAME).exists());
        assert_eq!(create("hello"), Ok(true));
        let found = find(&root, &root.join("libs")).unwrap();
        assert_eq!(
            (found.name.as_str(), &found.entry),
            ("hello", &root.join("src/main.peep"))
        );
        assert_eq!(std::fs::read_to_string(&found.entry).unwrap(), HELLO);
        // A second create keeps the project as it is.
        let error = create("other").unwrap_err();
        assert!(error.ends_with("peeper.toml: already exists"), "{error}");
        assert_eq!(find(&root, &root.join("libs")).unwrap().name, "hello");
        // A main.peep already there is kept.
        std::fs::remove_file(root.join(manifest::FILE_NAME)).unwrap();
        std::fs::write(&found.entry, "fn main() {}").unwrap();
        assert_eq!(create("hello"), Ok(false));
        assert_eq!(
            std::fs::read_to_string(&found.entry).unwrap(),
            "fn main() {}"
        );
    }

    #[test]
    fn loads_nested_imports() {
        let project = write(
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
        let program = load(&project, &HashMap::<PathBuf, String>::new());
        assert_eq!(messages(&program), Vec::<String>::new());
        assert_eq!(
            order(&program).join(" "),
            "core:global myapp:b/c myapp:a myapp:main"
        );
        assert_eq!(
            imports(&program, module_named(&program, "myapp:main")),
            ["a=myapp:a", "c2=myapp:b/c", "=core:global"]
        );
        assert!(
            module_named(&program, "core:global").imports.is_empty(),
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
        let project = write(
            "errors",
            &[
                ("src/main.peep", &main),
                ("src/a.peep", ""),
                ("src/b.peep", "fn f( {"), // a syntax error belongs to b's own diagnostics
                ("src/c.peep", ""),
            ],
        );
        let program = load(&project, &HashMap::<PathBuf, String>::new());
        let missing = project.root.join("src/missing.peep");
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
        assert!(!program.checkable);
        let b = messages(&program)
            .into_iter()
            .find(|m| m.starts_with("myapp:b: "));
        let expected_token = format!("myapp:b: {} ", diagnostics::EXPECTED_TOKEN.id);
        assert!(b.is_some_and(|b| b.starts_with(&expected_token)));
    }

    #[test]
    fn reports_imports_at_their_line() {
        let project = write(
            "line",
            &[("src/main.peep", "fn main() {}\nimport \"myapp:missing\";\n")],
        );
        let program = load(&project, &HashMap::<PathBuf, String>::new());
        let main = module_named(&program, "myapp:main");
        assert_eq!(program.diagnostics.len(), 1, "{:?}", messages(&program));
        let label = &program.diagnostics[0].labels[0];
        assert_eq!(main.ast.file.position(label.span.start as usize), (2, 1));
    }

    #[test]
    fn reports_a_cycle_at_the_import_closing_it() {
        let project = write(
            "cycle",
            &[
                ("src/main.peep", r#"import "myapp:a";"#),
                ("src/a.peep", r#"import "myapp:b";"#),
                ("src/b.peep", r#"import "myapp:a";"#),
            ],
        );
        let program = load(&project, &HashMap::<PathBuf, String>::new());
        assert_eq!(
            messages(&program),
            ["myapp:b: M0002 cyclic import detected: myapp:a -> myapp:b -> myapp:a"]
        );
        let label = &program.diagnostics[0].labels[0];
        assert!(Arc::ptr_eq(
            &label.file,
            &module_named(&program, "myapp:b").ast.file
        ));
    }

    /// A cycle through the prelude, closed by a written import of it, is
    /// reported once, at that import.
    #[test]
    fn a_cycle_through_the_prelude_is_reported_once() {
        let project = write(
            "prelude_cycle",
            &[
                ("src/main.peep", "fn main() {}"),
                ("libs/core/src/global.peep", r#"import "core:x";"#),
                ("libs/core/src/x.peep", r#"import "core:global";"#),
            ],
        );
        let program = load(&project, &HashMap::<PathBuf, String>::new());
        let cycles: Vec<_> = program
            .diagnostics
            .iter()
            .filter(|d| d.code == diagnostics::CYCLIC_IMPORT)
            .collect();
        let messages: Vec<_> = cycles.iter().map(|d| &d.message).collect();
        assert_eq!(
            messages,
            ["cyclic import detected: core:global -> core:x -> core:global"]
        );
        let label = &cycles[0].labels[0];
        assert!(Arc::ptr_eq(
            &label.file,
            &module_named(&program, "core:x").ast.file
        ));
    }

    #[test]
    fn redundant_prelude_import() {
        let project = write(
            "prelude",
            &[
                ("src/main.peep", r#"import "core:global";"#),
                ("libs/core/src/global.peep", ""),
            ],
        );
        let program = load(&project, &HashMap::<PathBuf, String>::new());
        assert_eq!(
            messages(&program),
            ["myapp:main: S0004 `core:global` is imported automatically"]
        );
        // the implicit import is kept beside the written one
        assert_eq!(
            imports(&program, module_named(&program, "myapp:main")),
            ["global=core:global", "=core:global"]
        );
    }

    #[test]
    fn a_single_file_cannot_import() {
        let mut project = write(
            "single",
            &[
                ("tool.peep", "import \"myapp:a\";\nfn main() {}"),
                ("libs/core/src/global.peep", ""),
            ],
        );
        project.entry = project.root.join("tool.peep");
        let program = load(&project, &HashMap::<PathBuf, String>::new());
        assert_eq!(
            messages(&program),
            ["tool: M0003 a single file cannot import; check its project folder instead"]
        );
        assert_eq!(
            imports(&program, module_named(&program, "tool")),
            ["=core:global"],
            "the prelude still comes in"
        );
    }

    #[test]
    fn unreadable_and_non_utf8_entries() {
        let mut project = write("unreadable", &[("src/bad.peep", "fn \u{0} main")]);
        std::fs::write(project.root.join("src/bad.peep"), b"fn \xff main() {}").unwrap();
        let program = load(&project, &HashMap::<PathBuf, String>::new());
        let got = messages(&program);
        assert!(
            got.len() == 1 && got[0].starts_with("program: M0001 read module: "),
            "{got:?}"
        );
        project.entry = project.root.join("src/bad.peep");
        let program = load(&project, &HashMap::<PathBuf, String>::new());
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
        let project = write("many", &files);
        let first = load(&project, &HashMap::<PathBuf, String>::new());
        assert!(messages(&first).is_empty());
        assert_eq!(first.modules.len(), 202);
        for (index, module) in first.modules.iter().enumerate() {
            for import in &module.imports {
                assert!(
                    import.module.index() < index,
                    "{} before {}",
                    module.path,
                    first.modules[import.module.index()].path
                );
            }
        }
        for _ in 0..5 {
            assert_eq!(
                order(&load(&project, &HashMap::<PathBuf, String>::new())),
                order(&first),
                "order differs between runs"
            );
        }
    }

    #[test]
    fn open_files_win_over_disk() {
        let project = write(
            "open",
            &[
                ("src/main.peep", "import \"myapp:a\";\nfn main() {}"),
                ("src/a.peep", "fn A( {"),
            ],
        );
        let open_files = HashMap::from([
            (
                project.root.join("src/a.peep"),
                "import \"myapp:b\";\nfn A() {}".to_string(),
            ),
            (project.root.join("src/b.peep"), "fn B() {}".to_string()), // not on disk
        ]);
        let program = load(&project, &open_files);
        assert_eq!(messages(&program), Vec::<String>::new());
        assert_eq!(module_named(&program, "myapp:b").ast.file.text, "fn B() {}");
    }

    #[test]
    fn path_of_a_file() {
        let project = write("path", &[]);
        assert_eq!(
            path_of(&project.packages, &project.root.join("src/deep/x.peep")),
            ("myapp:deep/x".to_string(), true)
        );
        assert_eq!(
            path_of(&project.packages, &project.root.join("tool.peep")),
            ("tool".to_string(), false)
        );
        assert_eq!(
            resolve(&project.packages, "myapp:a.b.peep"),
            Ok(("myapp:a.b".to_string(), project.root.join("src/a.b.peep")))
        );
    }
}
