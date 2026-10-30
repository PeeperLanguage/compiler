//! Answering the editor's requests: hover, go to definition and code
//! actions.

use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use lsp_server::ErrorCode;
use lsp_server::Request;
use lsp_server::RequestId;
use lsp_server::Response;
use lsp_types::CodeAction;
use lsp_types::CodeActionKind;
use lsp_types::CodeActionOrCommand;
use lsp_types::CodeActionParams;
use lsp_types::Location;
use lsp_types::TextDocumentPositionParams;
use lsp_types::Url;
use model::ast;
use model::semantic_types::builtin_doc;
use source::SourceFile;
use source::Span;

use crate::checked::CachedFact;
use crate::checked::CachedKind;
use crate::checked::Checked;
use crate::convert::offset;
use crate::convert::range;
use crate::host;
use crate::hover::declaration_hover;
use crate::hover::doc_hover;
use crate::hover::hover_text;
use crate::hover::markdown_hover;
use crate::server::Server;
use crate::server::file_path;
impl Server<'_> {
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
        let definition = method == "textDocument/definition";
        // A keyword, an operator or a literal is a token, not a name: asked
        // first, so `/` in `a/2` is not read as the end of `a`.
        if !definition && let Some(hover) = self.token_doc_at(&place) {
            return Response::new_ok(id, markdown_hover(hover));
        }
        let answer = self.name_at(&place).and_then(|(checked, module, name)| {
            if matches!(name.kind, CachedKind::Builtin) {
                if definition {
                    return None; // declared nowhere
                }
                let written = module.ast.file.slice(name.span);
                let doc = builtin_doc(written, host().pointer_bits)?;
                return markdown_hover(doc_hover(written, &doc));
            }
            let (file, declared, hover) = declaration_of(checked, module, name)?;
            if definition {
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
}

/// The file and span where the name `fact`, found in `module`, is declared,
/// and what hover shows for it. A built-in name has no declaration.
fn declaration_of<'a>(
    checked: &'a Checked,
    module: &'a project::Module,
    fact: CachedFact,
) -> Option<(&'a Arc<SourceFile>, Span, String)> {
    let CachedFact { span, kind } = fact;
    let program = checked.program.as_ref()?;
    match kind {
        CachedKind::Variable { mutable, ty } => {
            let (file, declared) = (&module.ast.file, span);
            let name = file.slice(declared);
            let binding = if mutable {
                format!("mut {name}: {ty}")
            } else {
                format!("{name}: {ty}")
            };
            let hover = hover_text(file, &module.path, declared, &binding, None, None);
            Some((file, declared, hover))
        }
        CachedKind::Constant { text } => {
            let (tree, declared) = (&module.ast, span);
            let mut declarations = tree.decls.ids();
            let declaration = declarations
                .find(|&declaration| tree.decls[declaration].name().span == declared)?;
            let hover = declaration_hover(tree, declaration, &module.path, text.as_ref());
            Some((&tree.file, declared, hover))
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
            Some((&tree.file, declared, hover))
        }
        CachedKind::Local { .. } => unreachable!("`name_at` gives a local's declaration"),
        CachedKind::Literal { .. } => unreachable!("`name_at` gives no literal"),
        CachedKind::Module(file) => {
            let imported = program.module_of(&file)?;
            // In a string: a highlighter would read `06_x` as a number.
            let hover = format!("```peeper\nimport \"{}\"\n```\n", imported.path);
            Some((&imported.ast.file, Span::at(0), hover))
        }
        CachedKind::Builtin => unreachable!("`answer_request` answers a built-in name itself"),
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
