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
use std::panic::AssertUnwindSafe;
use std::path::Component;
use std::path::Path;
use std::path::PathBuf;
use std::path::Prefix;
use std::sync::Mutex;
use std::sync::mpsc;

use diagnostics::Diagnostic;
use model::ModuleId;
use model::program::Program;
use packages::SOURCE_EXTENSION;
use packages::file_of;
use packages::import_path_of;
use packages::is_source_file;

use crate::loading::Job;
use crate::loading::load_module;
use crate::order::sort_modules;

/// Imported by every module except itself.
const PRELUDE_PATH: &str = "core:global";
/// The folder of an installation that holds the bundled libraries;
/// scripts/build.sh and .github/workflows/release.yml spell it too.
pub const LIBRARIES_FOLDER: &str = "libs";

/// The folder of a package that holds its source files.
const SOURCE_FOLDER: &str = "src";

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
fn entry(source: &Path) -> PathBuf {
    source.join(format!("main{SOURCE_EXTENSION}"))
}

/// The entry file a new project starts with.
const NEW_PROJECT_MAIN: &str = "fn main() {\n    println(\"Hello from Peeper!\");\n}\n";

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
    write_new(&manifest, &manifest::new_program(name)).map_err(|error| match error.kind() {
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
/// folder in it is a library (`library_packages`). No libraries folder
/// means no bundled libraries.
fn bundled_libraries(libraries: &Path) -> Result<HashMap<String, PathBuf>, String> {
    let libraries =
        canonical_path(libraries).map_err(|error| format!("{}: {error}", libraries.display()))?;
    let entries = match std::fs::read_dir(&libraries) {
        Ok(entries) => entries,
        Err(error) if error.kind() == ErrorKind::NotFound => return Ok(HashMap::new()),
        Err(error) => return Err(format!("{}: {error}", libraries.display())),
    };
    let mut folders = Vec::new();
    for entry in entries {
        let entry = entry.map_err(|error| format!("{}: {error}", libraries.display()))?;
        // A file beside the libraries, such as a README, is none; a link to
        // a library's folder is one.
        if entry.path().is_dir() {
            folders.push(entry.path());
        }
    }
    library_packages(folders.into_iter().map(|folder| {
        let manifest = manifest::read(&folder);
        (folder, manifest)
    }))
}

/// Maps the name of each bundled library to its src folder, from each
/// library's folder and what its peeper.toml says, read from a disk or
/// from text a page holds. The error is a full message: a folder whose
/// peeper.toml is missing or bad, is no library, or names a library
/// another folder names too would quietly leave out a library every
/// program may need, such as core.
pub fn library_packages(
    libraries: impl IntoIterator<Item = (PathBuf, Result<manifest::Manifest, String>)>,
) -> Result<HashMap<String, PathBuf>, String> {
    let mut found = HashMap::new();
    for (folder, manifest) in libraries {
        let manifest = manifest?;
        let file = folder.join(manifest::FILE_NAME);
        if manifest.build != manifest::Build::Library {
            return Err(manifest::bundled_not_library(&file));
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
        let open = (open_files.keys()).any(|file| file.starts_with(source) && is_source_file(file));
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
                    // A panic goes to the main thread, which would
                    // otherwise wait for this module forever.
                    let loaded = std::panic::catch_unwind(AssertUnwindSafe(|| {
                        load_module(&project.packages, open_files, job, with_prelude)
                    }));
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
        for file in open_files.keys().filter(|file| is_source_file(file)) {
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
                // This thread holds `send_module`, so a receive always ends.
                let loaded = done.recv().expect("this thread holds a sender");
                // Unwinding drops `send_job`, which ends the other workers.
                loaded.unwrap_or_else(|panic| std::panic::resume_unwind(panic))
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
        } else if is_source_file(&path) {
            files.push(path);
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests;
