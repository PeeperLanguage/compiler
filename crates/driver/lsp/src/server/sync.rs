//! Keeping the open files current, checking their programs and
//! publishing the diagnostics.

use std::collections::HashMap;
use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;
use std::time::Instant;

use diagnostics::fix;
use lsp_server::Message;
use lsp_server::Notification;
use lsp_types::CodeAction;
use lsp_types::CodeActionKind;
use lsp_types::DidChangeTextDocumentParams;
use lsp_types::DidCloseTextDocumentParams;
use lsp_types::DidOpenTextDocumentParams;
use lsp_types::DidSaveTextDocumentParams;
use lsp_types::PublishDiagnosticsParams;
use lsp_types::Url;

use crate::BoxError;
use crate::DEBOUNCE;
use crate::OpenFile;
use crate::convert::lsp_diagnostics;
use crate::convert::workspace_edit;
use crate::server::Server;
use crate::server::file_path;
impl Server<'_> {
    /// Keeps the open files' text current and checks the program of the file
    /// the notification is about: at once for an opened, saved or closed file
    /// (a closed file's text is the disk's again), after `DEBOUNCE` for a
    /// changed one. Other notifications, and documents that are not local
    /// files, are ignored.
    pub(crate) fn handle_notification(
        &mut self,
        notification: Notification,
    ) -> Result<(), BoxError> {
        let Notification { method, params } = notification;
        let (file, debounce) = match method.as_str() {
            "textDocument/didOpen" => {
                let params: DidOpenTextDocumentParams = serde_json::from_value(params)?;
                let file = file_path(&params.text_document.uri);
                if let Some(file) = &file {
                    let open = OpenFile {
                        text: params.text_document.text,
                        version: params.text_document.version,
                    };
                    self.open_files.insert(file.clone(), open);
                }
                (file, false)
            }
            "textDocument/didChange" => {
                let mut params: DidChangeTextDocumentParams = serde_json::from_value(params)?;
                let file = file_path(&params.text_document.uri);
                // Full sync: each change holds the whole text; the last wins.
                if let (Some(file), Some(change)) = (&file, params.content_changes.pop()) {
                    let open = OpenFile {
                        text: change.text,
                        version: params.text_document.version,
                    };
                    self.open_files.insert(file.clone(), open);
                }
                (file, true)
            }
            "textDocument/didSave" => {
                let params: DidSaveTextDocumentParams = serde_json::from_value(params)?;
                (file_path(&params.text_document.uri), false)
            }
            "textDocument/didClose" => {
                let params: DidCloseTextDocumentParams = serde_json::from_value(params)?;
                let file = file_path(&params.text_document.uri);
                if let Some(file) = &file {
                    self.open_files.remove(file);
                }
                (file, false)
            }
            _ => (None, false),
        };
        let Some(file) = file else {
            return Ok(());
        };
        if debounce {
            self.pending.insert(file);
            self.deadline = Some(Instant::now() + DEBOUNCE);
            return Ok(());
        }
        // Other pending files of its programs are still checked once more.
        self.pending.remove(&file);
        for project in self.projects_holding(&file) {
            self.check(project, &file)?;
        }
        Ok(())
    }

    /// Checks the program of each pending file, once per program.
    pub(crate) fn check_pending(&mut self) -> Result<(), BoxError> {
        self.deadline = None;
        let mut programs = HashMap::new();
        for file in std::mem::take(&mut self.pending) {
            for project in self.projects_holding(&file) {
                programs
                    .entry(project.entry.clone())
                    .or_insert((project, file.clone()));
            }
        }
        for (project, file) in programs.into_values() {
            self.check(project, &file)?;
        }
        Ok(())
    }

    /// The projects to check after `file` changed: its own (`project_of`),
    /// and each one whose program last checked holds it, as every program
    /// holds a bundled library's module; by entry file, once each.
    fn projects_holding(&self, file: &Path) -> Vec<project::Project> {
        let holding = (self.checked.iter())
            .filter(|(_, checked)| {
                let program = checked.program.as_ref();
                program.is_some_and(|program| program.module_of(file).is_some())
            })
            .map(|(entry, _)| entry.as_path());
        let mut projects: Vec<project::Project> = std::iter::once(file)
            .chain(holding)
            .filter_map(|file| self.project_of(file))
            .collect();
        projects.sort_unstable_by(|one, other| one.entry.cmp(&other.entry));
        projects.dedup_by(|one, other| one.entry == other.entry);
        projects
    }

    /// The project `file` belongs to: the one of the nearest folder above it
    /// holding peeper.toml, or else the file alone. `None`, after logging why,
    /// when it cannot be read.
    pub(crate) fn project_of(&self, file: &Path) -> Option<project::Project> {
        let folder = file
            .ancestors()
            .skip(1)
            .find(|folder| folder.join(project::manifest::FILE_NAME).is_file());
        match project::find(folder.unwrap_or(file), self.libraries) {
            Ok(project) => Some(project),
            Err(message) => {
                eprintln!("peeper lsp: {message}"); // the editor's server log
                None
            }
        }
    }

    /// Checks the program of `project`, after a change to `file`, and
    /// publishes the diagnostics of its files, and an empty list for each file
    /// that had some last time and has none now.
    pub(crate) fn check(&mut self, project: project::Project, file: &Path) -> Result<(), BoxError> {
        let program = project::load(&project, &self.open_files);
        let mut checked = self.checked.remove(&project.entry).unwrap_or_default();
        let mut all: Vec<&diagnostics::Diagnostic> = program.diagnostics.iter().collect();
        // A program that cannot be checked keeps the last compile for the next.
        if program.checkable {
            checked.update(&program);
            all.extend(checked.results.diagnostics());
        }
        let (published, fix_all) = self.publish(all, &checked.published, file, &project.root)?;
        checked.published = published;
        checked.fix_all = fix_all;
        if program.checkable {
            checked.program = Some(program);
        }
        self.checked.insert(project.entry, checked);
        Ok(())
    }

    /// Publishes `diagnostics` by file, and an empty list for each file of
    /// `published` (those that had some last time) that has none now. A
    /// diagnostic about no place in a file, such as an unreadable module, is
    /// shown at the start of `changed`, the file that changed. Gives the files
    /// shown, and each file's sure fixes as one action for a later
    /// `source.fixAll` request, chosen as `peeper fix` chooses them for the
    /// project at `root`.
    pub(crate) fn publish(
        &self,
        diagnostics: Vec<&diagnostics::Diagnostic>,
        published: &HashSet<PathBuf>,
        changed: &Path,
        root: &Path,
    ) -> Result<(HashSet<PathBuf>, HashMap<PathBuf, CodeAction>), BoxError> {
        let mut by_file: HashMap<PathBuf, Vec<&diagnostics::Diagnostic>> = HashMap::new();
        for diagnostic in diagnostics {
            let place = diagnostic
                .labels
                .first()
                .map_or(changed.to_path_buf(), |label| {
                    PathBuf::from(&label.file.path)
                });
            by_file.entry(place).or_default().push(diagnostic);
        }
        let fix_all = by_file
            .iter()
            .filter_map(|(file, in_file)| {
                let fixes = fix::chosen(in_file.iter().copied(), root);
                let fixes = fixes.into_iter().map(|(fix, _)| fix);
                let action = CodeAction {
                    title: "Apply all sure fixes".to_string(),
                    kind: Some(CodeActionKind::SOURCE_FIX_ALL),
                    edit: Some(workspace_edit(
                        fixes,
                        &self.open_files,
                        self.versioned_edits,
                    )?),
                    ..CodeAction::default()
                };
                Some((file.clone(), action))
            })
            .collect();
        let shown: HashSet<PathBuf> = by_file.keys().cloned().collect();
        for file in shown.union(published) {
            let Ok(uri) = Url::from_file_path(file) else {
                continue; // not an absolute path
            };
            let in_file = by_file.remove(file).unwrap_or_default();
            let diagnostics = in_file
                .into_iter()
                .flat_map(|diagnostic| {
                    lsp_diagnostics(diagnostic, &self.open_files, self.versioned_edits)
                })
                .collect();
            let params = PublishDiagnosticsParams {
                uri,
                diagnostics,
                version: None,
            };
            let notification =
                Notification::new("textDocument/publishDiagnostics".to_string(), params);
            self.connection
                .sender
                .send(Message::Notification(notification))?;
        }
        Ok((shown, fix_all))
    }
}
