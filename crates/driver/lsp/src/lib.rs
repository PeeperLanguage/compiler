//! The language server: LSP over a connection (`peeper lsp` uses stdin and
//! stdout). It keeps the text of the files open in the editor and checks the
//! program a file belongs to when the file is opened, saved or closed, and
//! once typing pauses (`DEBOUNCE`) after changes; then it publishes the
//! diagnostics of the program's files, each with its help lines' fixes as
//! quick fixes. Go to definition and hover over a name read what the last
//! check recorded. Only the protocol is here: `project`
//! finds and loads the program and `compile` checks it.

mod checked;
mod convert;
mod hover;
mod server;

use std::error::Error;
use std::path::Path;
use std::time::Duration;
use std::time::Instant;

use lsp_server::Connection;
use lsp_server::Message;
use lsp_types::CodeActionKind;
use lsp_types::CodeActionOptions;
use lsp_types::CodeActionProviderCapability;
use lsp_types::HoverProviderCapability;
use lsp_types::OneOf;
use lsp_types::ServerCapabilities;
use lsp_types::TextDocumentSyncCapability;
use lsp_types::TextDocumentSyncKind;
use model::machine::Machine;

use crate::server::Server;

type BoxError = Box<dyn Error + Send + Sync>;

/// How long typing must pause before changed files are checked: a check per
/// keystroke costs more than it shows (gopls waits the same way, its
/// `diagnosticsDelay`).
const DEBOUNCE: Duration = Duration::from_millis(200);

/// Serves the editor on stdin and stdout until it shuts the server down.
/// `libraries` holds the bundled libraries, as for `project::find`.
pub fn serve(libraries: &Path) -> Result<(), BoxError> {
    Machine::host()?; // the checks are for it
    let (connection, io_threads) = Connection::stdio();
    serve_connection(connection, libraries)?;
    io_threads.join()?;
    Ok(())
}

/// How many bits a pointer has on the host, which the editor checks for;
/// `serve` does not start on a machine with no target.
fn host_pointer_bits() -> u32 {
    Machine::host()
        .expect("`serve` found the host")
        .pointer_bits
}

/// Serves `connection` until the client shuts the server down. It takes the
/// connection so it drops on return, which lets the IO threads end before
/// `serve` joins them.
fn serve_connection(connection: Connection, libraries: &Path) -> Result<(), BoxError> {
    let capabilities = ServerCapabilities {
        text_document_sync: Some(TextDocumentSyncCapability::Kind(TextDocumentSyncKind::FULL)),
        definition_provider: Some(OneOf::Left(true)),
        hover_provider: Some(HoverProviderCapability::Simple(true)),
        code_action_provider: Some(CodeActionProviderCapability::Options(CodeActionOptions {
            code_action_kinds: Some(vec![
                CodeActionKind::QUICKFIX,
                CodeActionKind::SOURCE_FIX_ALL,
            ]),
            ..CodeActionOptions::default()
        })),
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
                let response = server.answer_request(request);
                connection.sender.send(Message::Response(response))?;
            }
            Message::Notification(notification) => {
                // A bad message is the client's mistake; serving goes on.
                if let Err(error) = server.handle_notification(notification) {
                    eprintln!("peeper lsp: {error}");
                }
            }
            Message::Response(_) => {} // the server sends no requests
        }
    }
}

/// A file open in the editor: its text, and the version the editor gave that
/// text, which a quick fix names so the editor refuses it once the text moves
/// on.
struct OpenFile {
    text: String,
    version: i32,
}

/// `project::load` reads an open file's text.
impl AsRef<str> for OpenFile {
    fn as_ref(&self) -> &str {
        &self.text
    }
}

#[cfg(test)]
mod tests;
