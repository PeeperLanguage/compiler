//! Loading one module: reading and parsing its file on a worker thread, and
//! resolving its imports to the modules they name.

use std::collections::HashMap;
use std::ffi::OsString;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use diagnostics::Diagnostic;
use diagnostics::fix::Fix;
use diagnostics::suggest::nearest_name;
use model::ModuleId;
use model::ast::Tree;
use source::SourceFile;
use source::Span;

use crate::Import;
use crate::ImportKind;
use crate::Job;
use crate::Loaded;
use crate::Module;
use crate::PRELUDE_PATH;
use crate::default_alias;
use crate::packages::SOURCE_EXTENSION;
use crate::packages::resolve;
use crate::path_text;

/// Reads, parses and resolves the imports of one module, taking its text from
/// `open_files` when it is open there. A file that cannot be read, or is not
/// UTF-8, gets an empty tree and one error.
pub(crate) fn load_file(
    packages: &HashMap<String, PathBuf>,
    open_files: &HashMap<PathBuf, impl AsRef<str>>,
    job: Job,
    with_prelude: bool,
) -> Loaded {
    let path = path_text(&job.file);
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
    pub(crate) fn into_module(self, ids: &HashMap<String, ModuleId>) -> Module {
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

    pub(crate) fn resolve_imports(
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
            // A guess that the import meant the path `meant`, if any.
            let guess = |diagnostic: Diagnostic, meant: Option<String>| match meant {
                // Written back as a literal only when it needs no escape.
                Some(meant)
                    if !meant.contains(['"', '\\']) && !meant.contains(char::is_control) =>
                {
                    let literal = format!("\"{meant}\"");
                    let message = format!("did you mean {literal}?");
                    let fix = Fix {
                        file: file.clone(),
                        span: import.path,
                        new_text: literal,
                    };
                    diagnostic.guess(message, vec![fix])
                }
                _ => diagnostic,
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
                Err((message, meant)) => {
                    let diagnostic = error(diagnostics::INVALID_IMPORT_PATH, message);
                    self.diagnostics.push(guess(diagnostic, meant));
                    self.imports_complete = false;
                    continue;
                }
            };
            if !imported_file.is_file() && !open_files.contains_key(&imported_file) {
                let message = format!("module not found: {}", imported_file.display());
                let diagnostic = error(diagnostics::MODULE_NOT_FOUND, message);
                let meant = nearest_module(&imported_file, &path, &self.path, open_files);
                self.diagnostics.push(guess(diagnostic, meant));
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
/// The import path of the module nearest to the missing `imported_file`,
/// imported as `path` from the module `importer`: a module in the same
/// folder, on disk or open in an editor, other than `importer`, which would
/// import itself.
fn nearest_module(
    imported_file: &Path,
    path: &str,
    importer: &str,
    open_files: &HashMap<PathBuf, impl AsRef<str>>,
) -> Option<String> {
    let folder = imported_file.parent()?;
    let written_name = imported_file.file_stem()?.to_str()?;
    let folder_path = &path[..path.rfind(['/', ':'])? + 1];
    let on_disk = std::fs::read_dir(folder).into_iter().flatten().flatten();
    let open = open_files
        .keys()
        .filter(|open| open.parent() == Some(folder));
    let mut names: Vec<String> = on_disk
        .map(|entry| entry.file_name())
        .chain(open.filter_map(|open| open.file_name()).map(OsString::from))
        .filter_map(|name| {
            let name = name.into_string().ok()?;
            Some(name.strip_suffix(SOURCE_EXTENSION)?.to_string())
        })
        .filter(|name| format!("{folder_path}{name}") != importer)
        .collect();
    names.sort();
    names.dedup(); // an open file is on disk too
    let candidates = names.iter().map(|name| (name.as_str(), 0));
    let nearest = nearest_name(written_name, candidates)?;
    Some(format!("{folder_path}{nearest}"))
}
