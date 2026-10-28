//! Turning the compiler's diagnostics, fixes and spans into LSP values.

use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;

use diagnostics::NoteKind;
use diagnostics::Severity;
use diagnostics::fix::Fix;
use lsp_types::CodeAction;
use lsp_types::CodeActionKind;
use lsp_types::DiagnosticRelatedInformation;
use lsp_types::DiagnosticSeverity;
use lsp_types::DocumentChanges;
use lsp_types::Location;
use lsp_types::NumberOrString;
use lsp_types::OneOf;
use lsp_types::OptionalVersionedTextDocumentIdentifier;
use lsp_types::Position;
use lsp_types::Range;
use lsp_types::TextDocumentEdit;
use lsp_types::TextEdit;
use lsp_types::Url;
use lsp_types::WorkspaceEdit;
use source::SourceFile;
use source::Span;

use crate::OpenFile;

/// Each help line of `diagnostic` that has fixes, as one quick fix whose
/// edits apply together. Each file's edits name the version of the open text
/// they were made for (`open_files`), so the editor refuses them once that
/// text has changed. A help line with a fix in a file that has no URI is left
/// out, so a fix is never applied in part.
pub(crate) fn quick_fixes(
    diagnostic: &diagnostics::Diagnostic,
    open_files: &HashMap<PathBuf, OpenFile>,
) -> Vec<CodeAction> {
    let fixable = diagnostic
        .notes
        .iter()
        .filter(|note| !note.fixes().is_empty());
    fixable
        .filter_map(|note| {
            Some(CodeAction {
                title: note.text.clone(),
                kind: Some(CodeActionKind::QUICKFIX),
                is_preferred: Some(note.has_sure_fix()),
                edit: Some(workspace_edit(note.fixes(), open_files)?),
                ..CodeAction::default()
            })
        })
        .collect()
}
/// `fixes` as one edit of the files they touch, each file's edits naming the
/// version of the open text they were made for (`open_files`). `None` when a
/// fix is in a file that has no URI, or there are no fixes.
pub(crate) fn workspace_edit<'f>(
    fixes: impl IntoIterator<Item = &'f Fix>,
    open_files: &HashMap<PathBuf, OpenFile>,
) -> Option<WorkspaceEdit> {
    let mut documents: Vec<TextDocumentEdit> = Vec::new();
    for fix in fixes {
        let uri = Url::from_file_path(&fix.file.path).ok()?;
        let edit = OneOf::Left(TextEdit {
            range: range(&fix.file, fix.span),
            new_text: fix.new_text.clone(),
        });
        if let Some(document) = documents
            .iter_mut()
            .find(|document| document.text_document.uri == uri)
        {
            document.edits.push(edit);
        } else {
            let open = open_files.get(Path::new(&fix.file.path));
            let text_document = OptionalVersionedTextDocumentIdentifier {
                uri,
                version: open.map(|open| open.version), // none: not open
            };
            documents.push(TextDocumentEdit {
                text_document,
                edits: vec![edit],
            });
        }
    }
    (!documents.is_empty()).then(|| WorkspaceEdit {
        document_changes: Some(DocumentChanges::Edits(documents)),
        ..WorkspaceEdit::default()
    })
}
/// `diagnostic` in LSP form: the primary label gives the range, the other
/// labels are related information, the notes follow the message, and the
/// quick fixes go in `data` for a later code action request. Then a hint
/// for each span of hint labels with text in the primary label's file.
pub(crate) fn lsp_diagnostics(
    diagnostic: &diagnostics::Diagnostic,
    open_files: &HashMap<PathBuf, OpenFile>,
) -> Vec<lsp_types::Diagnostic> {
    let mut message = diagnostic.message.clone();
    for note in &diagnostic.notes {
        message.push_str(match note.kind {
            NoteKind::Note => "\nnote: ",
            NoteKind::Help(_) | NoteKind::Guess(_) => "\nhelp: ",
        });
        message.push_str(&note.text);
    }
    let primary = diagnostic
        .labels
        .first()
        .map_or(Range::default(), |label| range(&label.file, label.span));
    // Labels on one span are one entry with their texts in order, as the
    // terminal draws them, so the editor's hover and inline text agree.
    let mut related: Vec<DiagnosticRelatedInformation> = Vec::new();
    let hint_labels = (diagnostic.labels.iter()).filter(|label| label.severity == Severity::Hint);
    for label in hint_labels {
        let Ok(uri) = Url::from_file_path(&label.file.path) else {
            continue;
        };
        let location = Location {
            uri,
            range: range(&label.file, label.span),
        };
        let Some(entry) = related.iter_mut().find(|entry| entry.location == location) else {
            related.push(DiagnosticRelatedInformation {
                location,
                message: label.message.clone(),
            });
            continue;
        };
        if !entry.message.is_empty() && !label.message.is_empty() {
            entry.message.push_str(", ");
        }
        entry.message.push_str(&label.message);
    }
    let severity = |severity| match severity {
        Severity::Error => DiagnosticSeverity::ERROR,
        Severity::Warning => DiagnosticSeverity::WARNING,
        Severity::Info => DiagnosticSeverity::INFORMATION,
        Severity::Hint => DiagnosticSeverity::HINT,
    };
    let code = Some(NumberOrString::String(diagnostic.code.id.to_string()));
    let source = Some("peeper".to_string());
    // A hint points back to its diagnostic, as rust-analyzer's do, so an
    // editor shows it as part of it: Zed then draws the label as a hint
    // (grey), not as related information (blue).
    let primary_uri =
        (diagnostic.labels.first()).and_then(|label| Url::from_file_path(&label.file.path).ok());
    let hints = (related.iter())
        .filter(|entry| Some(&entry.location.uri) == primary_uri.as_ref())
        .filter(|entry| !entry.message.is_empty())
        .map(|entry| lsp_types::Diagnostic {
            range: entry.location.range,
            severity: Some(severity(Severity::Hint)),
            code: code.clone(),
            source: source.clone(),
            message: entry.message.clone(),
            related_information: Some(vec![DiagnosticRelatedInformation {
                location: Location {
                    uri: entry.location.uri.clone(),
                    range: primary,
                },
                message: diagnostic.message.clone(),
            }]),
            ..lsp_types::Diagnostic::default()
        })
        .collect::<Vec<_>>();
    let main = lsp_types::Diagnostic {
        range: primary,
        severity: Some(severity(diagnostic.code.severity)),
        code,
        source,
        message,
        related_information: Some(related),
        data: match quick_fixes(diagnostic, open_files) {
            fixes if fixes.is_empty() => None,
            fixes => serde_json::to_value(fixes).ok(),
        },
        ..lsp_types::Diagnostic::default()
    };
    std::iter::once(main).chain(hints).collect()
}
pub(crate) fn range(file: &SourceFile, span: Span) -> Range {
    Range {
        start: position(file, span.start as usize),
        end: position(file, span.end as usize),
    }
}
/// The LSP position of `offset` in `file`: a 0-based line, and a column in
/// UTF-16 code units, as LSP counts them by default.
pub(crate) fn position(file: &SourceFile, offset: usize) -> Position {
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
pub(crate) fn offset(file: &SourceFile, position: Position) -> usize {
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
