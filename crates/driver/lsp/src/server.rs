//! The server: the files open in the editor, and what it does on each
//! notification and request.

use std::collections::HashMap;
use std::collections::HashSet;
use std::path::Path;
use std::path::PathBuf;
use std::time::Instant;

use diagnostics::fix;
use lsp_server::Connection;
use lsp_server::ErrorCode;
use lsp_server::Message;
use lsp_server::Notification;
use lsp_server::Request;
use lsp_server::RequestId;
use lsp_server::Response;
use lsp_types::CodeAction;
use lsp_types::CodeActionKind;
use lsp_types::CodeActionOrCommand;
use lsp_types::CodeActionParams;
use lsp_types::DidChangeTextDocumentParams;
use lsp_types::DidCloseTextDocumentParams;
use lsp_types::DidOpenTextDocumentParams;
use lsp_types::DidSaveTextDocumentParams;
use lsp_types::Location;
use lsp_types::PublishDiagnosticsParams;
use lsp_types::TextDocumentPositionParams;
use lsp_types::Url;
use model::ast;
use model::semantic_types::builtin_doc;
use source::Span;

use crate::BoxError;
use crate::DEBOUNCE;
use crate::OpenFile;
use crate::checked::CachedFact;
use crate::checked::CachedKind;
use crate::checked::Checked;
use crate::convert::lsp_diagnostics;
use crate::convert::offset;
use crate::convert::range;
use crate::convert::workspace_edit;
use crate::host;
use crate::hover::declaration_hover;
use crate::hover::doc_hover;
use crate::hover::hover_text;
use crate::hover::markdown_hover;

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

    /// Answers go to definition with where the name at the given place is
    /// declared, and hover with what it is, or what the keyword there does;
    /// both answer null when neither is there. Other requests are unknown.
    pub(crate) fn answer_request(&self, request: Request) -> Response {
        let Request { id, method, params } = request;
        let place: TextDocumentPositionParams = match method.as_str() {
            "textDocument/codeAction" => return self.answer_code_actions(id, params),
            // Both requests' parameters hold the place, and more to ignore.
            "textDocument/definition" | "textDocument/hover" => {
                match serde_json::from_value(params) {
                    Ok(place) => place,
                    Err(error) => return invalid_params(id, &error),
                }
            }
            _ => {
                let code = ErrorCode::MethodNotFound as i32;
                return Response::new_err(id, code, format!("unknown request `{method}`"));
            }
        };
        // A keyword, an operator or a literal is a token, not a name: asked
        // first, so `/` in `a/2` is not read as the end of `a`.
        if method == "textDocument/hover"
            && let Some(hover) = self.token_doc_at(&place)
        {
            return Response::new_ok(id, markdown_hover(hover));
        }
        let answer = self.name_at(&place).and_then(|(checked, module, name)| {
            let CachedFact { span, kind } = name;
            let program = checked.program.as_ref()?;
            let (file, declared, hover) = match kind {
                CachedKind::Variable { mutable, ty } => {
                    let (file, declared) = (&module.ast.file, span);
                    let name = file.slice(declared);
                    let binding = if mutable {
                        format!("mut {name}: {ty}")
                    } else {
                        format!("{name}: {ty}")
                    };
                    let hover = hover_text(file, &module.path, declared, &binding, None, None);
                    (file, declared, hover)
                }
                CachedKind::Constant { text } => {
                    let (tree, declared) = (&module.ast, span);
                    let mut declarations = tree.decls.ids();
                    let declaration = declarations
                        .find(|&declaration| tree.decls[declaration].name().span == declared)?;
                    let hover = declaration_hover(tree, declaration, &module.path, text.as_ref());
                    (&tree.file, declared, hover)
                }
                CachedKind::Declaration { file, position } => {
                    let declared_in = program.module_of(&file)?;
                    let tree = &declared_in.ast;
                    // In range: a module whose declarations changed changes
                    // its interface, so its importers' names are found again.
                    let declaration = tree.declaration(position);
                    let declared = tree.decls[declaration].name().span;
                    // A module constant's checked value is cached with its name.
                    let mut facts = checked.facts.get(&file).into_iter().flatten();
                    let constant = facts.find_map(|fact| match fact.kind {
                        CachedKind::Constant { ref text } if fact.span == declared => text.as_ref(),
                        _ => None,
                    });
                    let hover = declaration_hover(tree, declaration, &declared_in.path, constant);
                    (&tree.file, declared, hover)
                }
                CachedKind::Local { .. } => unreachable!("`name_at` gives a local's declaration"),
                CachedKind::Literal { .. } => unreachable!("`name_at` gives no literal"),
                CachedKind::Module(file) => {
                    let imported = program.module_of(&file)?;
                    // In a string: a highlighter would read `06_x` as a number.
                    let hover = format!("```peeper\nimport \"{}\"\n```\n", imported.path);
                    (&imported.ast.file, Span::at(0), hover)
                }
                CachedKind::Builtin => {
                    if method == "textDocument/definition" {
                        return None; // declared nowhere
                    }
                    let written = module.ast.file.slice(span);
                    let doc = builtin_doc(written, host().pointer_bits)?;
                    return markdown_hover(doc_hover(written, &doc));
                }
            };
            if method == "textDocument/definition" {
                let uri = Url::from_file_path(&file.path).ok()?;
                let range = range(file, declared);
                return serde_json::to_value(Location { uri, range }).ok();
            }
            markdown_hover(hover)
        });
        Response::new_ok(id, answer)
    }

    /// What hover shows for the keyword, operator or literal at `place`, if
    /// one is there: its text, or a literal's type, as a Peeper block, then
    /// what it is.
    pub(crate) fn token_doc_at(&self, place: &TextDocumentPositionParams) -> Option<String> {
        let file = file_path(&place.text_document.uri)?;
        let (checked, module) = self.modules_of(&file).next()?;
        let tree = &module.ast;
        let offset = offset(&tree.file, place.position) as u32;
        // Only the lexer tells a keyword from the same word in a comment or
        // a string.
        let (tokens, _) = lexer::scan(&tree.file);
        let mut tokens = tokens.iter();
        let token = tokens.find(|token| token.span.start <= offset && offset < token.span.end)?;
        // Which operator a token is (`-` subtracts or negates) the tree
        // tells: a binary one sits between its operands, a unary one starts
        // its expression. It wins over the token's own doc, which is for its
        // other uses (`&` is a bit-and or a reference).
        let (start, end) = (token.span.start, token.span.end);
        let operator = |expression| match tree.exprs[expression] {
            ast::Expr::Binary { op, left, right }
                if tree.exprs.span(left).end <= start && end <= tree.exprs.span(right).start =>
            {
                Some((op.symbol(), op.doc()))
            }
            ast::Expr::Unary { op, .. } if tree.exprs.span(expression).start == start => {
                Some((op.symbol(), op.doc()))
            }
            _ => None,
        };
        let mut operators = tree.exprs.ids().filter_map(operator);
        if let Some((shown, doc)) = operators.find(|&(symbol, _)| symbol == token.kind.text()) {
            return Some(doc_hover(shown, doc));
        }
        let doc = token.kind.doc()?;
        // A literal shows the type its check gave it.
        let mut facts = checked.facts.get(&file).into_iter().flatten();
        let literal = facts.find_map(|fact| match fact.kind {
            CachedKind::Literal { ref ty } if fact.span == token.span => Some(ty.as_str()),
            _ => None,
        });
        Some(doc_hover(literal.unwrap_or(token.kind.text()), doc))
    }

    /// The module read from `file` in each program last checked that holds
    /// it, by the program's entry file, with what that check keeps. A module
    /// read from other text than the editor now has, before a check or after
    /// one that could not load, is left out: its places are not the
    /// editor's.
    pub(crate) fn modules_of<'a>(
        &'a self,
        file: &Path,
    ) -> impl Iterator<Item = (&'a Checked, &'a project::Module)> {
        let open = self.open_files.get(file);
        let mut programs: Vec<(&PathBuf, &Checked)> = self.checked.iter().collect();
        programs.sort_unstable_by_key(|&(entry, _)| entry);
        programs.into_iter().filter_map(move |(_, checked)| {
            let module = checked.program.as_ref()?.module_of(file)?;
            let current = open.is_none_or(|open| open.text == module.ast.file.text);
            current.then_some((checked, module))
        })
    }

    /// The name at `place` and what it stands for: a name the last check of
    /// a program holding its file recorded, a local's use giving the local's
    /// declaration, which holds what is known of it; else the name of one of
    /// the module's declarations; with what that check keeps and the file's
    /// module. A name holds the bytes of its span, and else one ending at
    /// `place` is taken, as a cursor right after a word is on it.
    pub(crate) fn name_at(
        &self,
        place: &TextDocumentPositionParams,
    ) -> Option<(&Checked, &project::Module, CachedFact)> {
        let file = file_path(&place.text_document.uri)?;
        let holds: [fn(Span, u32) -> bool; 2] = [
            |span, offset| span.start <= offset && offset < span.end,
            |span, offset| span.end == offset,
        ];
        holds.into_iter().find_map(|holds| {
            self.modules_of(&file).find_map(|(checked, module)| {
                let offset = offset(&module.ast.file, place.position) as u32;
                let contains_offset = |span: Span| holds(span, offset);
                let facts = checked.facts.get(&file).map_or(&[][..], Vec::as_slice);
                let mut names = facts.iter().filter(|fact| {
                    !matches!(fact.kind, CachedKind::Literal { .. }) && contains_offset(fact.span)
                });
                if let Some(name) = names.next() {
                    let name = match name.kind {
                        CachedKind::Local { declared } => {
                            facts.iter().find(|fact| fact.span == declared)?
                        }
                        _ => name,
                    };
                    return Some((checked, module, name.clone()));
                }
                let tree = &module.ast;
                let declarations = tree.declarations();
                let declared = declarations.enumerate().find_map(|(index, declaration)| {
                    let span = tree.decls[declaration].name().span;
                    contains_offset(span).then_some((index, span))
                });
                let (position, span) = declared?;
                let kind = CachedKind::Declaration {
                    file: file.clone(),
                    position: position as u32,
                };
                Some((checked, module, CachedFact { span, kind }))
            })
        })
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

    /// Answers a code action request with the kinds it asks for: the quick
    /// fixes of the diagnostics it names, which the published diagnostics
    /// carry in their `data`, and the file's sure fixes as one `source.fixAll`
    /// action. A fix-on-save request asks for `source.fixAll` only, so it gets
    /// no guesses. An editor that takes no versioned edits cannot refuse a
    /// fix made for older text, so it gets none until its text is checked.
    pub(crate) fn answer_code_actions(&self, id: RequestId, params: serde_json::Value) -> Response {
        let params: CodeActionParams = match serde_json::from_value(params) {
            Ok(params) => params,
            Err(error) => return invalid_params(id, &error),
        };
        let mut actions = Vec::new();
        let file = file_path(&params.text_document.uri);
        let checked = file
            .as_ref()
            .is_some_and(|file| self.modules_of(file).next().is_some());
        if !self.versioned_edits && !checked {
            return Response::new_ok(id, actions);
        }
        let only = params.context.only.as_ref();
        let named = params.context.diagnostics.into_iter();
        for diagnostic in named.filter(|_| asks_for(only, &CodeActionKind::QUICKFIX)) {
            let fixes = diagnostic
                .data
                .clone()
                .map(serde_json::from_value::<Vec<CodeAction>>);
            for action in fixes.and_then(Result::ok).unwrap_or_default() {
                actions.push(CodeActionOrCommand::CodeAction(CodeAction {
                    diagnostics: Some(vec![diagnostic.clone()]),
                    ..action
                }));
            }
        }
        let fix_all = file
            .filter(|_| asks_for(only, &CodeActionKind::SOURCE_FIX_ALL))
            .and_then(|file| {
                (self.modules_of(&file))
                    .find_map(|(checked, _)| checked.fix_all.get(&file))
                    .cloned()
            });
        actions.extend(fix_all.map(CodeActionOrCommand::CodeAction));
        Response::new_ok(id, actions)
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
/// The answer to the request `id` whose parameters are not what it takes.
pub(crate) fn invalid_params(id: RequestId, error: &serde_json::Error) -> Response {
    Response::new_err(id, ErrorCode::InvalidParams as i32, error.to_string())
}
/// Whether a code action request that asks only for the kinds `only` (all
/// kinds when `None`) asks for `kind`: a kind covers the kinds below it, as
/// `source` covers `source.fixAll`.
pub(crate) fn asks_for(only: Option<&Vec<CodeActionKind>>, kind: &CodeActionKind) -> bool {
    let wanted = kind.as_str();
    only.is_none_or(|only| {
        only.iter().any(|asked| {
            let asked = asked.as_str();
            wanted == asked
                || wanted
                    .strip_prefix(asked)
                    .is_some_and(|rest| rest.starts_with('.'))
        })
    })
}
/// The file `uri` names, spelled as `project::canonical_path` spells it, so
/// it matches the paths of the program's files.
pub(crate) fn file_path(uri: &Url) -> Option<PathBuf> {
    project::canonical_path(&uri.to_file_path().ok()?).ok()
}
