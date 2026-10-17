//! The language server: LSP over a connection (`peeper lsp` uses stdin and
//! stdout). It keeps the text of the files open in the editor and checks the
//! program a file belongs to when the file is opened, saved or closed, and
//! once typing pauses (`DEBOUNCE`) after changes; then it publishes the
//! diagnostics of the program's files. Only the protocol is here: `project`
//! finds and loads the program and `build` checks it.

use std::collections::HashMap;
use std::collections::HashSet;
use std::error::Error;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use std::time::Instant;

use diagnostics::Severity;
use lsp_server::Connection;
use lsp_server::ErrorCode;
use lsp_server::Message;
use lsp_server::Notification;
use lsp_server::Response;
use lsp_types::DiagnosticRelatedInformation;
use lsp_types::DiagnosticSeverity;
use lsp_types::DidChangeTextDocumentParams;
use lsp_types::DidCloseTextDocumentParams;
use lsp_types::DidOpenTextDocumentParams;
use lsp_types::DidSaveTextDocumentParams;
use lsp_types::Location;
use lsp_types::NumberOrString;
use lsp_types::Position;
use lsp_types::PublishDiagnosticsParams;
use lsp_types::Range;
use lsp_types::ServerCapabilities;
use lsp_types::TextDocumentSyncCapability;
use lsp_types::TextDocumentSyncKind;
use lsp_types::Url;
use source::SourceFile;
use source::Span;

type BoxError = Box<dyn Error + Send + Sync>;

/// How long typing must pause before changed files are checked: a check per
/// keystroke costs more than it shows (gopls waits the same way, its
/// `diagnosticsDelay`).
const DEBOUNCE: Duration = Duration::from_millis(200);

/// Serves the editor on stdin and stdout until it shuts the server down.
/// `libraries` holds the bundled libraries, as for `project::find`.
pub fn serve(libraries: &Path) -> Result<(), BoxError> {
    let (connection, io_threads) = Connection::stdio();
    run(connection, libraries)?;
    io_threads.join()?;
    Ok(())
}

/// Serves `connection` until the client shuts the server down.
fn run(connection: Connection, libraries: &Path) -> Result<(), BoxError> {
    let capabilities = ServerCapabilities {
        text_document_sync: Some(TextDocumentSyncCapability::Kind(TextDocumentSyncKind::FULL)),
        ..ServerCapabilities::default()
    };
    connection.initialize(serde_json::to_value(capabilities)?)?;
    let mut server = Server::new(&connection, libraries);
    loop {
        let message = match server.deadline {
            None => match connection.receiver.recv() {
                Ok(message) => message,
                Err(_) => return Ok(()), // the client is gone
            },
            Some(deadline) => {
                let wait = deadline.saturating_duration_since(Instant::now());
                match connection.receiver.recv_timeout(wait) {
                    Ok(message) => message,
                    Err(error) if error.is_timeout() => {
                        server.check_pending()?;
                        continue;
                    }
                    Err(_) => return Ok(()),
                }
            }
        };
        match message {
            Message::Request(request) => {
                if connection.handle_shutdown(&request)? {
                    return Ok(());
                }
                let message = format!("unknown request `{}`", request.method);
                let code = ErrorCode::MethodNotFound as i32;
                let response = Response::new_err(request.id, code, message);
                connection.sender.send(Message::Response(response))?;
            }
            Message::Notification(notification) => {
                // A bad message is the client's mistake; serving goes on.
                if let Err(error) = server.notification(notification) {
                    eprintln!("peeper lsp: {error}");
                }
            }
            Message::Response(_) => {} // the server sends no requests
        }
    }
}

/// What the server keeps between messages.
struct Server<'s> {
    connection: &'s Connection,
    libraries: &'s Path,
    open_files: HashMap<PathBuf, String>, // as `project::load` takes them
    checked: HashMap<PathBuf, Checked>,   // by the entry file of each program
    /// Files changed and not checked yet; their programs are checked once
    /// `deadline` passes.
    pending: HashSet<PathBuf>,
    deadline: Option<Instant>,
}

impl<'s> Server<'s> {
    fn new(connection: &'s Connection, libraries: &'s Path) -> Server<'s> {
        Server {
            connection,
            libraries,
            open_files: HashMap::new(),
            checked: HashMap::new(),
            pending: HashSet::new(),
            deadline: None,
        }
    }

    /// Keeps the open files' text current and checks the program of the file
    /// the notification is about: at once for an opened, saved or closed file
    /// (a closed file's text is the disk's again), after `DEBOUNCE` for a
    /// changed one. Other notifications, and documents that are not local
    /// files, are ignored.
    fn notification(&mut self, notification: Notification) -> Result<(), BoxError> {
        let Notification { method, params } = notification;
        let (file, debounce) = match method.as_str() {
            "textDocument/didOpen" => {
                let params: DidOpenTextDocumentParams = serde_json::from_value(params)?;
                let file = params.text_document.uri.to_file_path().ok();
                if let Some(file) = &file {
                    self.open_files
                        .insert(file.clone(), params.text_document.text);
                }
                (file, false)
            }
            "textDocument/didChange" => {
                let mut params: DidChangeTextDocumentParams = serde_json::from_value(params)?;
                let file = params.text_document.uri.to_file_path().ok();
                // Full sync: each change holds the whole text; the last wins.
                if let (Some(file), Some(change)) = (&file, params.content_changes.pop()) {
                    self.open_files.insert(file.clone(), change.text);
                }
                (file, true)
            }
            "textDocument/didSave" => {
                let params: DidSaveTextDocumentParams = serde_json::from_value(params)?;
                (params.text_document.uri.to_file_path().ok(), false)
            }
            "textDocument/didClose" => {
                let params: DidCloseTextDocumentParams = serde_json::from_value(params)?;
                let file = params.text_document.uri.to_file_path().ok();
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
        // Other pending files of its program are still checked once more.
        self.pending.remove(&file);
        match self.project_of(&file) {
            Some(project) => self.check(project, &file),
            None => Ok(()),
        }
    }

    /// Checks the program of each pending file, once per program.
    fn check_pending(&mut self) -> Result<(), BoxError> {
        self.deadline = None;
        let mut programs = HashMap::new();
        for file in std::mem::take(&mut self.pending) {
            if let Some(project) = self.project_of(&file) {
                programs
                    .entry(project.entry.clone())
                    .or_insert((project, file));
            }
        }
        for (project, file) in programs.into_values() {
            self.check(project, &file)?;
        }
        Ok(())
    }

    /// The project `file` belongs to: the one of the nearest folder above it
    /// holding peeper.toml, or else the file alone. `None`, after logging why,
    /// when it cannot be read.
    fn project_of(&self, file: &Path) -> Option<project::Project> {
        let folder = file
            .ancestors()
            .skip(1)
            .find(|folder| folder.join(manifest::FILE_NAME).is_file());
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
    fn check(&mut self, project: project::Project, file: &Path) -> Result<(), BoxError> {
        let program = project::load(&project.packages, &project.entry, &self.open_files);
        let Checked {
            mut compiled,
            published,
        } = self.checked.remove(&project.entry).unwrap_or_default();
        let mut all: Vec<&diagnostics::Diagnostic> = program.load_diagnostics().collect();
        // A program that failed to load keeps the last compile for the next.
        if !program.load_failed() {
            compiled = build::compile(&program, usize::BITS, compiled, |_, _, _, _| {});
            all.extend(compiled.diagnostics());
        }
        let mut by_file: HashMap<PathBuf, Vec<lsp_types::Diagnostic>> = HashMap::new();
        for diagnostic in all {
            // A diagnostic about no place in a file, such as an unreadable
            // module, is shown at the start of the file that changed.
            let place = diagnostic
                .labels
                .first()
                .map_or(file.to_path_buf(), |label| PathBuf::from(&label.file.name));
            by_file
                .entry(place)
                .or_default()
                .push(lsp_diagnostic(diagnostic));
        }
        let shown: HashSet<PathBuf> = by_file.keys().cloned().collect();
        for file in shown.union(&published) {
            let Ok(uri) = Url::from_file_path(file) else {
                continue; // not an absolute path
            };
            let params = PublishDiagnosticsParams {
                uri,
                diagnostics: by_file.remove(file).unwrap_or_default(),
                version: None,
            };
            let notification =
                Notification::new("textDocument/publishDiagnostics".to_string(), params);
            self.connection
                .sender
                .send(Message::Notification(notification))?;
        }
        let checked = Checked {
            compiled,
            published: shown,
        };
        self.checked.insert(project.entry, checked);
        Ok(())
    }
}

/// What the server keeps of a program between checks.
#[derive(Default)]
struct Checked {
    compiled: build::Compiled,   // reused by the next compile
    published: HashSet<PathBuf>, // the files last given diagnostics
}

/// `diagnostic` in LSP form: the primary label gives the range, the other
/// labels are related information, and the notes follow the message.
fn lsp_diagnostic(diagnostic: &diagnostics::Diagnostic) -> lsp_types::Diagnostic {
    let mut message = diagnostic.message.clone();
    for note in &diagnostic.notes {
        message.push_str(if note.help { "\nhelp: " } else { "\nnote: " });
        message.push_str(&note.text);
    }
    let primary = diagnostic
        .labels
        .first()
        .map_or(Range::default(), |label| range(&label.file, label.span));
    let related = diagnostic
        .labels
        .iter()
        .skip(1)
        .filter_map(|label| {
            Some(DiagnosticRelatedInformation {
                location: Location {
                    uri: Url::from_file_path(&label.file.name).ok()?,
                    range: range(&label.file, label.span),
                },
                message: label.message.clone(),
            })
        })
        .collect();
    let severity = match diagnostic.code.severity {
        Severity::Error => DiagnosticSeverity::ERROR,
        Severity::Warning => DiagnosticSeverity::WARNING,
        Severity::Info => DiagnosticSeverity::INFORMATION,
    };
    lsp_types::Diagnostic {
        range: primary,
        severity: Some(severity),
        code: Some(NumberOrString::String(diagnostic.code.id.to_string())),
        source: Some("peeper".to_string()),
        message,
        related_information: Some(related),
        ..lsp_types::Diagnostic::default()
    }
}

fn range(file: &SourceFile, span: Span) -> Range {
    Range {
        start: position(file, span.start as usize),
        end: position(file, span.end as usize),
    }
}

/// The LSP position of `offset` in `file`: a 0-based line, and a column in
/// UTF-16 code units, as LSP counts them by default.
fn position(file: &SourceFile, offset: usize) -> Position {
    let line = file.line(offset);
    let start = file.line_span(line).start as usize;
    let mut end = offset;
    while !file.text.is_char_boundary(end) {
        end -= 1; // a lexer span may end inside a character
    }
    let character = file.text[start..end].encode_utf16().count();
    Position::new(line as u32 - 1, character as u32)
}

#[cfg(test)]
mod tests {
    use std::path::Path;
    use std::sync::Arc;
    use std::time::Duration;
    use std::time::Instant;

    use diagnostics::Diagnostic;
    use lsp_server::Connection;
    use lsp_server::Message;
    use lsp_server::Notification;
    use lsp_server::Request;
    use lsp_server::RequestId;
    use lsp_types::DiagnosticSeverity;
    use lsp_types::NumberOrString;
    use lsp_types::Position;
    use lsp_types::PublishDiagnosticsParams;
    use lsp_types::Url;
    use serde_json::json;
    use source::SourceFile;
    use source::Span;

    use super::DEBOUNCE;
    use super::Server;
    use super::lsp_diagnostic;
    use super::position;
    use super::run;

    #[test]
    fn positions_count_utf16_units() {
        // Line 2 is "çd𝄞x": `ç` is 2 bytes and 1 unit, `𝄞` 4 bytes and 2 units.
        let file = SourceFile::new("t.peep", "a\nçd𝄞x\r\nlast");
        for (offset, line, character) in [
            (0, 0, 0),
            (2, 1, 0),
            (4, 1, 1),
            (5, 1, 2),
            (7, 1, 2), // inside `𝄞`: counted up to its start
            (9, 1, 4),
            (10, 1, 5),
            (12, 2, 0),
            (16, 2, 4),
        ] {
            assert_eq!(
                position(&file, offset),
                Position::new(line, character),
                "offset {offset}"
            );
        }
    }

    #[test]
    fn converts_labels_and_notes() {
        let file = Arc::new(SourceFile::new("/app/main.peep", "let x = 1;\nx = 2;\n"));
        let diagnostic = Diagnostic::new(diagnostics::UNREACHABLE_CODE, "unreachable code")
            .label(&file, Span::new(11, 16), "this never runs")
            .label(&file, Span::new(4, 5), "declared here")
            .note("a note")
            .help("a help line", Vec::new());
        let converted = lsp_diagnostic(&diagnostic);
        assert_eq!(converted.range.start, Position::new(1, 0));
        assert_eq!(converted.range.end, Position::new(1, 5));
        assert_eq!(converted.severity, Some(DiagnosticSeverity::WARNING));
        assert_eq!(
            converted.code,
            Some(NumberOrString::String("W0001".to_string()))
        );
        assert_eq!(
            converted.message,
            "unreachable code\nnote: a note\nhelp: a help line"
        );
        let related = converted.related_information.unwrap();
        assert_eq!(related.len(), 1);
        assert_eq!(related[0].message, "declared here");
        assert_eq!(related[0].location.range.start, Position::new(0, 4));
        assert_eq!(
            related[0].location.uri,
            Url::from_file_path("/app/main.peep").unwrap()
        );
    }

    /// Opening a file with an error publishes it; changing the open text to
    /// fix it clears it, though the file on disk still has the error.
    #[test]
    fn publishes_and_clears_diagnostics() {
        let folder = std::env::temp_dir().join(format!("peeper-lsp-{}", std::process::id()));
        _ = std::fs::remove_dir_all(&folder);
        std::fs::create_dir_all(&folder).unwrap();
        let file = folder.join("main.peep");
        let broken = "fn main() { a $ b; }\n";
        std::fs::write(&file, broken).unwrap();
        let uri = Url::from_file_path(&file).unwrap();

        let (server, client) = Connection::memory();
        let thread = std::thread::spawn(move || run(server, Path::new("")).unwrap());
        let send = |message: Message| client.sender.send(message).unwrap();
        let request = |id: i32, method: &str, params: serde_json::Value| {
            Message::Request(Request::new(
                RequestId::from(id),
                method.to_string(),
                params,
            ))
        };
        let notification = |method: &str, params: serde_json::Value| {
            Message::Notification(Notification::new(method.to_string(), params))
        };
        let published = || match client.receiver.recv().unwrap() {
            Message::Notification(notification)
                if notification.method == "textDocument/publishDiagnostics" =>
            {
                serde_json::from_value::<PublishDiagnosticsParams>(notification.params).unwrap()
            }
            other => panic!("not a publish: {other:?}"),
        };

        send(request(1, "initialize", json!({ "capabilities": {} })));
        let Message::Response(response) = client.receiver.recv().unwrap() else {
            panic!("no initialize response");
        };
        let result = response.result.unwrap();
        assert_eq!(result["capabilities"]["textDocumentSync"], json!(1));
        send(notification("initialized", json!({})));

        let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": broken });
        send(notification(
            "textDocument/didOpen",
            json!({ "textDocument": document }),
        ));
        let opened = published();
        assert_eq!(opened.uri, uri);
        let first = &opened.diagnostics[0];
        assert_eq!(
            first.code,
            Some(NumberOrString::String("L0001".to_string()))
        );
        assert_eq!(first.range.start, Position::new(0, 14));

        // Three quick changes: one check, after the pause, of the last text.
        let change = |version: i32, text: &str| {
            let change = json!({
                "textDocument": { "uri": uri, "version": version },
                "contentChanges": [{ "text": text }],
            });
            notification("textDocument/didChange", change)
        };
        send(change(2, "fn main() { a $ }\n"));
        send(change(3, "fn main() { $ }\n"));
        send(change(4, "fn main() {}\n"));
        let typed = Instant::now();
        let changed = published();
        assert!(typed.elapsed() >= DEBOUNCE, "checked before the pause");
        assert_eq!(changed.uri, uri);
        assert!(changed.diagnostics.is_empty(), "{:?}", changed.diagnostics);
        let more = client.receiver.recv_timeout(Duration::from_millis(50));
        assert!(more.is_err(), "a second publish: {more:?}");

        send(request(2, "shutdown", serde_json::Value::Null));
        assert!(matches!(
            client.receiver.recv().unwrap(),
            Message::Response(_)
        ));
        send(notification("exit", serde_json::Value::Null));
        thread.join().unwrap();
    }

    /// A change only waits; a save checks at once, before `notification`
    /// returns, and its file is no longer pending.
    #[test]
    fn a_save_checks_at_once() {
        let folder = std::env::temp_dir().join(format!("peeper-lsp-save-{}", std::process::id()));
        _ = std::fs::remove_dir_all(&folder);
        std::fs::create_dir_all(&folder).unwrap();
        let file = folder.join("main.peep");
        std::fs::write(&file, "fn main() {}\n").unwrap();
        let uri = Url::from_file_path(&file).unwrap();
        let (connection, client) = Connection::memory();
        let mut server = Server::new(&connection, Path::new(""));
        let change = json!({
            "textDocument": { "uri": uri, "version": 2 },
            "contentChanges": [{ "text": "fn main() { a $ b; }\n" }],
        });
        let change = Notification::new("textDocument/didChange".to_string(), change);
        server.notification(change).unwrap();
        assert!(
            client.receiver.try_recv().is_err(),
            "a change was checked at once"
        );
        assert!(server.deadline.is_some());
        let save = json!({ "textDocument": { "uri": uri } });
        let save = Notification::new("textDocument/didSave".to_string(), save);
        server.notification(save).unwrap();
        let Ok(Message::Notification(published)) = client.receiver.try_recv() else {
            panic!("the save did not publish at once");
        };
        let published: PublishDiagnosticsParams = serde_json::from_value(published.params).unwrap();
        assert_eq!(
            published.diagnostics.len(),
            1,
            "{:?}",
            published.diagnostics
        );
        assert!(server.pending.is_empty());
    }
}
