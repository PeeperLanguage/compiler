//! The server: the files open in the editor, and what it does on each
//! notification and request.

mod requests;
mod sync;

use std::collections::HashMap;
use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;
use std::time::Instant;

use lsp_server::Connection;
use lsp_types::Url;

use crate::OpenFile;
use crate::checked::Checked;

/// What the server keeps between messages.
pub(crate) struct Server<'s> {
    connection: &'s Connection,
    libraries: &'s Path,
    open_files: HashMap<PathBuf, OpenFile>,
    /// Whether the editor takes versioned edits (`workspace_edit`).
    versioned_edits: bool,
    pub(crate) checked: HashMap<PathBuf, Checked>, // by the entry file of each program
    /// Files changed and not checked yet; their programs are checked once
    /// `deadline` passes.
    pub(crate) pending: HashSet<PathBuf>,
    pub(crate) deadline: Option<Instant>,
}

impl<'s> Server<'s> {
    pub(crate) fn new(
        connection: &'s Connection,
        libraries: &'s Path,
        versioned_edits: bool,
    ) -> Server<'s> {
        Server {
            connection,
            libraries,
            open_files: HashMap::new(),
            versioned_edits,
            checked: HashMap::new(),
            pending: HashSet::new(),
            deadline: None,
        }
    }
}

/// The file `uri` names, spelled as `project::canonical_path` spells it, so
/// it matches the paths of the program's files.
pub(crate) fn file_path(uri: &Url) -> Option<PathBuf> {
    project::canonical_path(&uri.to_file_path().ok()?).ok()
}
