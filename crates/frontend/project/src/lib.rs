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
use model::semantic_types::TypeId;
use model::semantic_types::Types;
use packages::SOURCE_EXTENSION;
use packages::file_of;
use packages::import_path_of;
use source::SourceFile;
use source::Span;

use crate::loading::load_module;
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

/// The declaration `definition` of `modules` names.
pub fn declaration(modules: &[Module], definition: DefId) -> &ast::Decl {
    let tree = &modules[definition.module.index()].ast;
    &tree.decls[tree.declaration(definition.index)]
}

/// The text of the name the declaration `definition` of `modules` declares.
pub fn declared_text(modules: &[Module], definition: DefId) -> &str {
    let (name, file) = declared_name(modules, definition);
    name.text(file)
}

/// Whether other modules can use `name`: it starts with an upper case letter.
pub fn is_exported(name: &str) -> bool {
    name.starts_with(char::is_uppercase)
}

/// The name of the declaration `definition` of `modules` as a program calls
/// it: its own name, or `Owner::name` for a function of the type `Owner`.
pub fn declared_path(modules: &[Module], definition: DefId) -> String {
    let name = declared_text(modules, definition);
    match declaration(modules, definition) {
        ast::Decl::Function(ast::Function {
            owner: Some(owner), ..
        }) => {
            let file = &modules[definition.module.index()].ast.file;
            format!("{}::{name}", owner.text(file))
        }
        _ => name.to_string(),
    }
}

/// The type `ty` of `types` as a message names it, each declared type by the
/// name its declaration in `modules` gives it (`Types::text`).
pub fn type_text(modules: &[Module], types: &Types, ty: TypeId) -> String {
    types.text(ty, |definition| declared_text(modules, definition))
}

/// The name the declaration `definition` of `modules` declares, and the file
/// it is in.
pub fn declared_name(modules: &[Module], definition: DefId) -> (Ident, &Arc<SourceFile>) {
    (
        declaration(modules, definition).name(),
        &modules[definition.module.index()].ast.file,
    )
}

/// What a program prints when it stops with `message` at `span` of `file`,
/// the file's path read from the project's `root`.
pub fn panic_text(root: &Path, file: &SourceFile, message: &[u8], span: Span) -> Vec<u8> {
    let (line, column) = file.position(span.start as usize);
    let mut text = b"panic: ".to_vec();
    text.extend_from_slice(message);
    let path = Path::new(&file.path);
    let path = path.strip_prefix(root).unwrap_or(path);
    let place = format!("\n  --> {}:{line}:{column}\n", path.display());
    text.extend_from_slice(place.as_bytes());
    text
}

/// The name in `#[extern("name")]` on the function `definition` of
/// `modules`, if it has one: it is a C function, linked by that name. The
/// error is where an `extern` attribute of another shape, or a second one,
/// is written; the checker reports it, so later phases never see one.
pub fn extern_symbol(modules: &[Module], definition: DefId) -> Result<Option<String>, Span> {
    let tree = &modules[definition.module.index()].ast;
    let ast::Decl::Function(function) = declaration(modules, definition) else {
        return Ok(None);
    };
    let mut symbol = None;
    for attribute in tree.children.lists.items(function.metadata.attributes) {
        let span = tree.children.attributes.span(attribute);
        let attribute = &tree.children.attributes[attribute];
        if attribute.name.text(&tree.file) != "extern" {
            continue;
        }
        let mut arguments = tree.children.lists.items(attribute.arguments);
        let (None, Some(argument), None) = (&symbol, arguments.next(), arguments.next()) else {
            return Err(span);
        };
        let ast::Expr::String { .. } = tree.exprs[argument] else {
            return Err(span);
        };
        let text = tree.file.slice(tree.exprs.span(argument));
        symbol = Some(String::from_utf8_lossy(&literal::value(text)).into_owned());
    }
    Ok(symbol)
}

/// The names of the fields of the struct declaration `definition` of
/// `modules`, in order.
pub fn field_names(modules: &[Module], definition: DefId) -> impl Iterator<Item = &str> {
    let tree = &modules[definition.module.index()].ast;
    let &ast::Decl::Type(written) = declaration(modules, definition) else {
        unreachable!("`{definition:?}` is a type declaration")
    };
    let ast::Type::Struct(fields) = tree.types[written.ty] else {
        unreachable!("`{definition:?}` declares a struct")
    };
    let names = tree.children.lists.items(fields);
    names.map(move |field| tree.children.fields[field].name.text(&tree.file))
}

/// The method declarations of the interface `definition`, in order; `None`
/// when it declares something else. The parser puts only functions in one.
pub fn interface_methods(
    modules: &[Module],
    definition: DefId,
) -> Option<impl ExactSizeIterator<Item = &ast::Function>> {
    let tree = &modules[definition.module.index()].ast;
    let &ast::Decl::Type(written) = declaration(modules, definition) else {
        return None;
    };
    let ast::Type::Interface(methods) = tree.types[written.ty] else {
        return None;
    };
    Some(
        tree.children
            .lists
            .items(methods)
            .map(|method| match &tree.decls[method] {
                ast::Decl::Function(function) => function,
                _ => unreachable!("an interface holds functions"),
            }),
    )
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
    /// The module of the project's entry file, which must declare `main`;
    /// `None` for a library.
    pub entry: Option<ModuleId>,
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
    /// The file the program starts at; `None` for a library, whose every
    /// source file is loaded.
    pub entry: Option<PathBuf>,
    pub root: PathBuf, // the project folder, or the folder of the file
    pub name: String,  // the project's package name, or the file's name without `.peep`
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

/// The text a source file's `path`, one `canonical_path` gives, is shown and
/// told apart by: its parts joined with `/` on every system, which Windows
/// reads too, so messages, panics and debug info read the same on each.
pub fn path_text(path: &Path) -> String {
    path.to_string_lossy()
        .replace(std::path::MAIN_SEPARATOR, "/")
}

/// The project at `path`: a folder holding peeper.toml, whose program starts
/// at src/main.peep and whose library is each file of src, or a single file,
/// checked on its own with no imports.
/// `libraries` holds the bundled libraries, one folder each. The error is a
/// full message naming the file it is about.
pub fn find(path: &Path, libraries: &Path) -> Result<Project, String> {
    let path = canonical_path(path).map_err(|error| format!("{}: {error}", path.display()))?;
    let is_dir = std::fs::metadata(&path)
        .map_err(|error| format!("{}: {error}", path.display()))?
        .is_dir();
    let packages = bundled_libraries(libraries)?;
    if !is_dir {
        let root = path.parent().unwrap_or(Path::new("")).to_path_buf();
        let name = path
            .file_stem()
            .unwrap_or_default()
            .to_string_lossy()
            .into_owned();
        return Ok(Project {
            packages,
            entry: Some(path),
            root,
            name,
        });
    }
    let manifest = manifest::read(&path)?;
    project_in(path, manifest, packages)
}

/// The project in the folder `root`, which `manifest` describes, beside the
/// bundled libraries `packages`. A library of a bundled library's name is
/// that library being worked on, so it takes its place. The error is a full
/// message naming the file it is about.
pub fn project_in(
    root: PathBuf,
    manifest: manifest::Manifest,
    mut packages: HashMap<String, PathBuf>,
) -> Result<Project, String> {
    let library = manifest.build == manifest::Build::Library;
    if !library && packages.contains_key(&manifest.name) {
        let file = root.join(manifest::FILE_NAME);
        return Err(format!(
            "{}: project name `{}` is taken by a bundled library",
            file.display(),
            manifest.name
        ));
    }
    let source = root.join(SOURCE_FOLDER);
    let entry = (!library).then(|| entry(&source));
    packages.insert(manifest.name.clone(), source);
    Ok(Project {
        packages,
        entry,
        root,
        name: manifest.name,
    })
}

/// The entry file of the package whose source folder is `source`.
pub fn entry(source: &Path) -> PathBuf {
    source.join(format!("main{SOURCE_EXTENSION}"))
}

/// The entry file a new project starts with.
const NEW_PROJECT_MAIN: &str =
    "fn main() {\n    Write(Stdout, c\"Hello from Peeper!\\n\", 19);\n}\n";

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
    if bundled_libraries(libraries)?.contains_key(name) {
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
    match write_new(&main, NEW_PROJECT_MAIN) {
        Ok(()) => Ok(true),
        Err(error) if error.kind() == ErrorKind::AlreadyExists => Ok(false),
        Err(error) => Err(format!("{}: {error}", main.display())),
    }
}

/// Maps the name of each library in `libraries` to its src folder: each
/// folder in it is a library, whose peeper.toml has `build = "lib"`. No
/// libraries folder means no bundled libraries. The error is a full message:
/// a folder that is no library, or two libraries of one name, would quietly
/// leave out a library that every program may need, such as core.
fn bundled_libraries(libraries: &Path) -> Result<HashMap<String, PathBuf>, String> {
    let Ok(libraries) = canonical_path(libraries) else {
        return Ok(HashMap::new()); // an empty path names no folder
    };
    let entries = match std::fs::read_dir(&libraries) {
        Ok(entries) => entries,
        Err(error) if error.kind() == ErrorKind::NotFound => return Ok(HashMap::new()),
        Err(error) => return Err(format!("{}: {error}", libraries.display())),
    };
    let mut found = HashMap::new();
    for entry in entries {
        let entry = entry.map_err(|error| format!("{}: {error}", libraries.display()))?;
        if !entry.file_type().is_ok_and(|kind| kind.is_dir()) {
            continue; // a file beside the libraries, such as a README
        }
        let folder = entry.path();
        let manifest = manifest::read(&folder)?;
        let file = folder.join(manifest::FILE_NAME);
        if manifest.build != manifest::Build::Library {
            let message = "a bundled library needs `build = \"lib\"`";
            return Err(format!("{}: {message}", file.display()));
        }
        if let Some(other) = found.insert(manifest.name, folder.join(SOURCE_FOLDER)) {
            let other = other.parent().unwrap_or(&other).display();
            return Err(format!(
                "{}: {other} is a library of this name too",
                file.display()
            ));
        }
    }
    Ok(found)
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

/// Reads `project`'s entry, or each source file of a library, and every
/// module they import, directly or not. Files
/// are read and parsed by a pool of worker threads, or by this thread on wasm,
/// which cannot start one; only this thread touches the module map, and each
/// module is touched only by its worker until it is handed over.
/// An entry in no package's folder is checked on its own and cannot import.
///
/// `open_files` holds the text of files open in an editor (anything that
/// reads as text, such as a `String`), read instead of what is on disk. Its
/// keys, like `project`'s paths, are spelled as `canonical_path` spells them.
/// Each `.peep` file of a package in it is loaded too, imported or not, so an
/// editor checks the file it has open.
pub fn load(project: &Project, open_files: &HashMap<PathBuf, impl AsRef<str> + Sync>) -> Program {
    let with_prelude = file_of(&project.packages, PRELUDE_PATH)
        .is_some_and(|file| file.is_file() || open_files.contains_key(&file));
    let workers = if cfg!(target_family = "wasm") {
        0
    } else {
        std::thread::available_parallelism().map_or(1, |n| n.get())
    };

    let mut starts = Vec::new();
    // Why a library has no files to load.
    let unlisted_library = if let Some(entry) = &project.entry {
        starts.push(entry.clone());
        None
    } else {
        let source = &project.packages[&project.name];
        let listed = add_source_files(source, &mut starts);
        // A page's or an editor's files need no disk.
        let open = (open_files.keys()).any(|file| {
            file.starts_with(source) && file.to_string_lossy().ends_with(SOURCE_EXTENSION)
        });
        let message = match listed {
            _ if open => None,
            Err(error) => Some(format!("read library: {error}")),
            Ok(()) if starts.is_empty() => {
                Some(format!("no source files in {}", path_text(source)))
            }
            Ok(()) => None,
        };
        message.map(|message| Diagnostic::new(diagnostics::MODULE_NOT_FOUND, message))
    };
    let entry_path =
        (project.entry.as_ref()).map(|entry| import_path_of(&project.packages, entry).0);
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
                    let loaded = load_module(&project.packages, open_files, job, with_prelude);
                    _ = send_module.send(loaded);
                }
            });
        }

        let mut seen = HashSet::new();
        for file in starts {
            let (path, in_package) = import_path_of(&project.packages, &file);
            if seen.insert(path.clone()) {
                _ = send_job.send(Job {
                    path,
                    file,
                    standalone: !in_package,
                });
            }
        }
        // A module of the project that an editor has open is checked even
        // when no import reaches it from the entry.
        for file in open_files
            .keys()
            .filter(|file| file.to_string_lossy().ends_with(SOURCE_EXTENSION))
        {
            let (path, in_package) = import_path_of(&project.packages, file);
            if in_package && seen.insert(path.clone()) {
                _ = send_job.send(Job {
                    path,
                    file: file.clone(),
                    standalone: false,
                });
            }
        }
        // Each path enters `seen` once, and `modules` when it is loaded.
        while modules.len() < seen.len() {
            let loaded = if workers == 0 {
                let job = jobs.lock().expect("no worker holds the lock").try_recv();
                let job = job.expect("each pending module has a job");
                load_module(&project.packages, open_files, job, with_prelude)
            } else {
                let Ok(loaded) = done.recv() else { break };
                loaded
            };
            for (_, path) in &loaded.imports {
                if seen.insert(path.clone()) {
                    let file =
                        file_of(&project.packages, path).expect("an import's package is known");
                    _ = send_job.send(Job {
                        path: path.clone(),
                        file,
                        standalone: false,
                    });
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
    diagnostics.extend(unlisted_library);
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
        entry: entry_path.and_then(|path| ids.get(&path).copied()),
    }
}

/// Adds each source file in `folder` and the folders in it to `files`. The
/// error names the folder that could not be read.
fn add_source_files(folder: &Path, files: &mut Vec<PathBuf>) -> Result<(), String> {
    let unread = |error: std::io::Error| format!("{}: {error}", path_text(folder));
    // A link to a folder is not followed: one back up would load the same
    // files again on each way round.
    for entry in std::fs::read_dir(folder).map_err(unread)? {
        let entry = entry.map_err(unread)?;
        let path = entry.path();
        if entry.file_type().is_ok_and(|kind| kind.is_dir()) {
            add_source_files(&path, files)?;
        } else if path.to_string_lossy().ends_with(SOURCE_EXTENSION) {
            files.push(path);
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests;
