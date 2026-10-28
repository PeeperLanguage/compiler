//! Reads the source files of a program in parallel, resolves their imports,
//! and orders the modules so each comes after the modules it imports. `find`
//! gives the packages and entry file of a project folder or a single file.

mod loading;
pub mod manifest;
mod order;
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
use model::DefId;
use model::ModuleId;
use model::ast;
use model::ast::Ident;
use model::ast::Tree;
use packages::SOURCE_EXTENSION;
use packages::path_of;
use packages::resolve;
use source::SourceFile;
use source::Span;

use crate::loading::load_file;
use crate::order::sort_modules;

/// Imported by every module except itself.
const PRELUDE_PATH: &str = "core:global";
/// The folder of a package that holds its source files.
const SOURCE_FOLDER: &str = "src";

/// One source file, read and parsed. Its source file is `ast.file`. What
/// `load` read never changes; what a compile finds, its scope included, is
/// kept by the compile (`check::Checker`).
pub struct Module {
    pub path: String, // import path, such as "myapp:utils/file" or "core:global"
    pub ast: Tree,
    pub imports: Vec<Import>, // in source order; the implicit prelude import last, in every module but the prelude
}

/// The name the declaration `definition` of `modules` declares, and the file
/// it is in.
pub fn declared_name(modules: &[Module], definition: DefId) -> (Ident, &Arc<SourceFile>) {
    let tree = &modules[definition.module.index()].ast;
    (
        tree.decls[tree.declaration(definition.index)].name(),
        &tree.file,
    )
}

/// The name in `#[extern("name")]` on the function `definition` of
/// `modules`, if it has one: it is a C function, linked by that name.
pub fn extern_symbol(modules: &[Module], definition: DefId) -> Option<String> {
    let tree = &modules[definition.module.index()].ast;
    let ast::Decl::Function(function) = &tree.decls[tree.declaration(definition.index)] else {
        return None;
    };
    tree.children
        .lists
        .items(function.metadata.attributes)
        .find_map(|attribute| {
            let attribute = &tree.children.attributes[attribute];
            if attribute.name.text(&tree.file) != "extern" {
                return None;
            }
            let argument = tree.children.lists.items(attribute.arguments).next()?;
            let ast::Expr::String { .. } = tree.exprs[argument] else {
                return None;
            };
            let text = tree.file.slice(tree.exprs.span(argument));
            Some(String::from_utf8_lossy(&literal::value(text)).into_owned())
        })
}

/// The names of the fields of the struct declaration `definition` of
/// `modules`, in order.
pub fn field_names(modules: &[Module], definition: DefId) -> impl Iterator<Item = &str> {
    let tree = &modules[definition.module.index()].ast;
    let ast::Decl::Type(written) = tree.decls[tree.declaration(definition.index)] else {
        unreachable!("`{definition:?}` is a type declaration")
    };
    let ast::Type::Struct(fields) = tree.types[written.ty] else {
        unreachable!("`{definition:?}` declares a struct")
    };
    let names = tree.children.lists.items(fields);
    names.map(move |field| tree.children.fields[field].name.text(&tree.file))
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
    /// The project folder the program was loaded from (`Project::root`),
    /// which panic messages name its files from, so a program is the same
    /// wherever its project is.
    pub root: PathBuf,
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
const HELLO: &str = "fn main() {\n    Write(Stdout, c\"Hello from Peeper!\\n\", 19);\n}\n";

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
        root: project.root.clone(),
    }
}

#[cfg(test)]
mod tests;
