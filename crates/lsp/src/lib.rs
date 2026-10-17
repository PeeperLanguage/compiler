//! The language server: LSP over a connection (`peeper lsp` uses stdin and
//! stdout). It keeps the text of the files open in the editor and checks the
//! program a file belongs to when the file is opened, saved or closed, and
//! once typing pauses (`DEBOUNCE`) after changes; then it publishes the
//! diagnostics of the program's files. Go to definition and hover over a name
//! read what the last check recorded. Only the protocol is here: `project`
//! finds and loads the program and `build` checks it.

use std::collections::HashMap;
use std::collections::HashSet;
use std::error::Error;
use std::path::Path;
use std::path::PathBuf;
use std::time::Duration;
use std::time::Instant;

use arena::Id;
use diagnostics::Severity;
use lsp_server::Connection;
use lsp_server::ErrorCode;
use lsp_server::Message;
use lsp_server::Notification;
use lsp_server::Request;
use lsp_server::Response;
use lsp_types::DiagnosticRelatedInformation;
use lsp_types::DiagnosticSeverity;
use lsp_types::DidChangeTextDocumentParams;
use lsp_types::DidCloseTextDocumentParams;
use lsp_types::DidOpenTextDocumentParams;
use lsp_types::DidSaveTextDocumentParams;
use lsp_types::Hover;
use lsp_types::HoverContents;
use lsp_types::HoverProviderCapability;
use lsp_types::LanguageString;
use lsp_types::Location;
use lsp_types::MarkedString;
use lsp_types::NumberOrString;
use lsp_types::OneOf;
use lsp_types::Position;
use lsp_types::PublishDiagnosticsParams;
use lsp_types::Range;
use lsp_types::ServerCapabilities;
use lsp_types::TextDocumentPositionParams;
use lsp_types::TextDocumentSyncCapability;
use lsp_types::TextDocumentSyncKind;
use lsp_types::Url;
use model::syntax;
use model::thir;
use model::types::Types;
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
        definition_provider: Some(OneOf::Left(true)),
        hover_provider: Some(HoverProviderCapability::Simple(true)),
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
                let response = server.request(request);
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

    /// Answers go to definition with where the name at the given place is
    /// declared, and hover with a line about it; both answer null when no
    /// name is there. Other requests are unknown.
    fn request(&self, request: Request) -> Response {
        let Request { id, method, params } = request;
        let place: TextDocumentPositionParams = match method.as_str() {
            // Both requests' parameters hold the place, and more to ignore.
            "textDocument/definition" | "textDocument/hover" => {
                match serde_json::from_value(params) {
                    Ok(place) => place,
                    Err(error) => {
                        let code = ErrorCode::InvalidParams as i32;
                        return Response::new_err(id, code, error.to_string());
                    }
                }
            }
            _ => {
                let code = ErrorCode::MethodNotFound as i32;
                return Response::new_err(id, code, format!("unknown request `{method}`"));
            }
        };
        let answer = self.name_at(&place).and_then(|(program, module, name)| {
            let (file, declared, hover) = match &name.target {
                Target::InBody { declared, ty } => {
                    let file = &module.syntax.file;
                    (file, *declared, format!("{}: {ty}", file.slice(*declared)))
                }
                Target::Declaration { module, index } => {
                    let tree = &program.modules.get(module)?.syntax;
                    let declaration = Id::from_index(*index as usize);
                    let declared = tree.decls[declaration].name();
                    (&tree.file, declared, header(tree, declaration))
                }
                Target::Module(path) => {
                    let file = &program.modules.get(path)?.syntax.file;
                    (file, Span::at(0), format!("module `{path}`"))
                }
            };
            if method == "textDocument/definition" {
                let uri = Url::from_file_path(&file.name).ok()?;
                let range = range(file, declared);
                return serde_json::to_value(Location { uri, range }).ok();
            }
            let hover = LanguageString {
                language: "peeper".to_string(),
                value: hover,
            };
            let hover = Hover {
                contents: HoverContents::Scalar(MarkedString::LanguageString(hover)),
                range: None,
            };
            serde_json::to_value(hover).ok()
        });
        Response::new_ok(id, answer)
    }

    /// The name at `place` that the last check of a program holding its file
    /// recorded, with that program and the file's module.
    fn name_at(
        &self,
        place: &TextDocumentPositionParams,
    ) -> Option<(&project::Program, &project::Module, &Name)> {
        let file = place.text_document.uri.to_file_path().ok()?;
        self.checked.values().find_map(|checked| {
            let program = checked.program.as_ref()?;
            let module = program
                .modules
                .values()
                .find(|module| Path::new(&module.syntax.file.name) == file)?;
            let offset = offset(&module.syntax.file, place.position) as u32;
            let names = checked.names.get(&module.path)?;
            let at = |name: &&Name| name.span.start <= offset && offset <= name.span.end;
            Some((program, module, names.iter().find(at)?))
        })
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
            program: mut compiled_program,
            mut names,
        } = self.checked.remove(&project.entry).unwrap_or_default();
        let mut all: Vec<&diagnostics::Diagnostic> = program.load_diagnostics().collect();
        // A program that failed to load keeps the last compile for the next.
        let loaded = !program.load_failed();
        if loaded {
            let mut found: HashMap<&str, Vec<Name>> = HashMap::new();
            compiled = build::compile(
                &program,
                usize::BITS,
                compiled,
                |definition, body, _, types| {
                    let path = program.order[definition.module as usize].as_str();
                    let body_names = body
                        .names
                        .iter()
                        .map(|name| Name::new(&program, name, types));
                    found.entry(path).or_default().extend(body_names);
                },
            );
            // A module the compile reused gave no names; with its text as
            // before, the names found before still fit it.
            let unchanged = |path: &String| {
                let before = compiled_program
                    .as_ref()
                    .and_then(|before| before.modules.get(path));
                let now = program.modules.get(path);
                before
                    .zip(now)
                    .is_some_and(|(before, now)| before.syntax.file.text == now.syntax.file.text)
            };
            names.retain(|path, _| !found.contains_key(path.as_str()) && unchanged(path));
            names.extend(
                found
                    .into_iter()
                    .map(|(path, found)| (path.to_string(), found)),
            );
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
        if loaded {
            compiled_program = Some(program);
        }
        let checked = Checked {
            compiled,
            published: shown,
            program: compiled_program,
            names,
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
    /// The program as last compiled, and the names written in the bodies of
    /// each of its modules, by module path: what definition and hover read.
    program: Option<project::Program>,
    names: HashMap<String, Vec<Name>>,
}

/// A name written in a body and what it stands for, kept as long as its
/// module is reused: by path and place, not by the ids of one compile.
struct Name {
    span: Span,
    target: Target,
}

enum Target {
    /// A parameter, `let` or `const` of the body, declared at `declared`, with
    /// its type's text.
    InBody { declared: Span, ty: String },
    /// The declaration at `index` of the module at `module`. Its importer is
    /// reused only while the module's declarations keep their kinds and names
    /// (`build::compile`), so the index stays right.
    Declaration { module: String, index: u32 },
    /// The module at this path.
    Module(String),
}

impl Name {
    /// `name` of a body of `program`, whose types are in `types`.
    fn new(program: &project::Program, name: &thir::Name, types: &Types) -> Name {
        let path = |module: u32| program.order[module as usize].clone();
        let target = match name.target {
            thir::Target::InBody { declared, ty } => Target::InBody {
                declared,
                ty: types.text(ty),
            },
            thir::Target::Declaration(definition) => Target::Declaration {
                module: path(definition.module),
                index: definition.index,
            },
            thir::Target::Module(module) => Target::Module(path(module)),
        };
        Name {
            span: name.span,
            target,
        }
    }
}

/// What hover shows for `declaration`: a function's text up to its body, or
/// another declaration's first line.
fn header(tree: &syntax::Tree, declaration: Id<syntax::Decl>) -> String {
    let span = tree.decls.span(declaration);
    let text = match tree.decls[declaration] {
        syntax::Decl::Function(syntax::Function {
            body: Some(body), ..
        }) => &tree.file.text[span.start as usize..tree.blocks.span(body).start as usize],
        _ => tree.file.slice(span).lines().next().unwrap_or_default(),
    };
    text.trim().to_string()
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

/// The byte offset of LSP `position` in `file`, the inverse of `position`. A
/// column past its line's end is at the end.
fn offset(file: &SourceFile, position: Position) -> usize {
    let line = file.line_span(position.line as usize + 1);
    let mut units = 0;
    for (index, character) in file.slice(line).char_indices() {
        if units >= position.character as usize {
            return line.start as usize + index;
        }
        units += character.len_utf16();
    }
    line.end as usize
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
    use lsp_server::Response;
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
    use super::offset;
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
            if offset != 7 {
                let back = super::offset(&file, Position::new(line, character));
                assert_eq!(back, offset, "back from {line}:{character}");
            }
        }
        assert_eq!(
            offset(&file, Position::new(1, 99)),
            10,
            "past the line's end"
        );
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
    /// Go to definition and hover over a name answer where it is declared and
    /// its type, from the open text; elsewhere they answer null.
    #[test]
    fn answers_definition_and_hover() {
        let folder = std::env::temp_dir().join(format!("peeper-lsp-name-{}", std::process::id()));
        _ = std::fs::remove_dir_all(&folder);
        std::fs::create_dir_all(&folder).unwrap();
        let file = folder.join("main.peep");
        std::fs::write(&file, "fn main() {}\n").unwrap();
        let uri = Url::from_file_path(&file).unwrap();
        let (connection, _client) = Connection::memory();
        let mut server = Server::new(&connection, Path::new(""));
        let text = "fn main() {\n    let total: i64 = 1;\n    let more = total;\n}\n";
        let document = json!({ "uri": uri, "languageId": "peeper", "version": 1, "text": text });
        let open = Notification::new(
            "textDocument/didOpen".to_string(),
            json!({ "textDocument": document }),
        );
        server.notification(open).unwrap();
        let ask = |method: &str, line: u32, character: u32| {
            let place = json!({
                "textDocument": { "uri": uri },
                "position": { "line": line, "character": character },
            });
            let request = Request::new(RequestId::from(1), method.to_string(), place);
            let Response { result, error, .. } = server.request(request);
            assert!(error.is_none(), "{error:?}");
            result.unwrap()
        };
        let definition = ask("textDocument/definition", 2, 16);
        assert_eq!(definition["uri"], json!(uri));
        assert_eq!(
            definition["range"]["start"],
            json!({ "line": 1, "character": 8 })
        );
        assert_eq!(
            definition["range"]["end"],
            json!({ "line": 1, "character": 13 })
        );
        let hover = ask("textDocument/hover", 2, 16);
        assert_eq!(hover["contents"]["value"], json!("total: i64"));
        assert_eq!(ask("textDocument/hover", 0, 0), serde_json::Value::Null);
    }
    /// Names across modules: each kind of name answers where it is declared
    /// and what it is, in a body with an error too. After an edit that moves
    /// `Add` but leaves `utils`' declarations as they were, `main` is reused,
    /// and its use of `Add` still finds where `Add` is now.
    #[test]
    fn names_across_modules() {
        let folder =
            std::env::temp_dir().join(format!("peeper-lsp-modules-{}", std::process::id()));
        _ = std::fs::remove_dir_all(&folder);
        std::fs::create_dir_all(folder.join("src")).unwrap();
        std::fs::write(
            folder.join("peeper.toml"),
            "name = \"app\"\nbuild = \"bin\"\n",
        )
        .unwrap();
        let utils = "fn Add(a: i32, b: i32) -> i32 { return a + b; }\nconst Limit: u8 = 200;\n";
        let main = "import \"app:utils\";
fn main(count: i64) {
    let mut total = count;
    total = 2;
    let sum = utils::Add(1, 2);
    let wide = utils::Limit;
    let wrong: bool = total;
}
";
        let (main_file, utils_file) = (folder.join("src/main.peep"), folder.join("src/utils.peep"));
        std::fs::write(&main_file, main).unwrap();
        std::fs::write(&utils_file, utils).unwrap();
        let (connection, _client) = Connection::memory();
        let mut server = Server::new(&connection, Path::new(""));
        let notify = |server: &mut Server, method: &str, params: serde_json::Value| {
            server
                .notification(Notification::new(method.to_string(), params))
                .unwrap();
        };
        let open = |file: &Path, text: &str| {
            let uri = Url::from_file_path(file).unwrap();
            json!({ "textDocument": { "uri": uri, "languageId": "peeper", "version": 1, "text": text } })
        };
        notify(&mut server, "textDocument/didOpen", open(&main_file, main));
        // The place of the `nth` `needle` in `main`.
        let place = |needle: &str, nth: usize| {
            let at = main.match_indices(needle).nth(nth).unwrap().0;
            let position = position(&SourceFile::new("main.peep", main), at);
            json!({
                "textDocument": { "uri": Url::from_file_path(&main_file).unwrap() },
                "position": position,
            })
        };
        let ask = |server: &Server, method: &str, needle: &str, nth: usize| {
            let request = Request::new(RequestId::from(1), method.to_string(), place(needle, nth));
            let Response { result, error, .. } = server.request(request);
            assert!(error.is_none(), "{error:?}");
            result.unwrap()
        };
        let hover = |server: &Server, needle: &str, nth: usize| {
            ask(server, "textDocument/hover", needle, nth)["contents"]["value"].clone()
        };
        // Where the name is declared: its file's name and line.
        let declared = |server: &Server, needle: &str, nth: usize| {
            let found = ask(server, "textDocument/definition", needle, nth);
            let uri: Url = serde_json::from_value(found["uri"].clone()).unwrap();
            let file = uri.to_file_path().unwrap();
            let name = file.file_name().unwrap().to_str().unwrap().to_string();
            (name, found["range"]["start"]["line"].clone())
        };
        assert_eq!(hover(&server, "count", 1), json!("count: i64"));
        assert_eq!(
            declared(&server, "count", 1),
            ("main.peep".to_string(), json!(1))
        );
        assert_eq!(hover(&server, "total", 1), json!("total: i64"));
        assert_eq!(
            declared(&server, "total", 1),
            ("main.peep".to_string(), json!(2))
        );
        assert_eq!(hover(&server, "utils", 1), json!("module `app:utils`"));
        assert_eq!(
            hover(&server, "Add", 0),
            json!("fn Add(a: i32, b: i32) -> i32")
        );
        assert_eq!(
            declared(&server, "Add", 0),
            ("utils.peep".to_string(), json!(0))
        );
        assert_eq!(hover(&server, "Limit", 0), json!("const Limit: u8 = 200;"));
        // `main` has an error, and its names are still there.
        assert_eq!(hover(&server, "total", 2), json!("total: i64"));

        let moved = format!("// moved down\n{utils}");
        notify(
            &mut server,
            "textDocument/didOpen",
            open(&utils_file, &moved),
        );
        assert_eq!(
            declared(&server, "Add", 0),
            ("utils.peep".to_string(), json!(1))
        );
    }
}
