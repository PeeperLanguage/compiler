//! Hover text: what a name declares, and the docs of a keyword or built-in.

use std::fmt::Write;

use arena::Id;
use lsp_types::Hover;
use lsp_types::HoverContents;
use lsp_types::MarkupContent;
use lsp_types::MarkupKind;
use model::ast;
use source::SourceFile;
use source::Span;

use crate::checked::ConstantText;

/// What hover shows for `declaration` of the module at `module`
/// (`hover_text`), its attributes first: a constant as its name, type and
/// value, from `constant`, worked out from its initializer; a function up to
/// its body; another declaration, or a constant with an error, whole.
pub(crate) fn declaration_hover(
    tree: &ast::Tree,
    declaration: Id<ast::Decl>,
    module: &str,
    constant: Option<&ConstantText>,
) -> String {
    let span = tree.decls.span(declaration);
    let decl = &tree.decls[declaration];
    let metadata = decl.metadata();
    // The attributes are written before the declaration's span.
    let start = tree
        .children
        .lists
        .items(metadata.attributes)
        .next()
        .map_or(span.start, |first| {
            tree.children.attributes.span(first).start
        });
    let written = |end: u32| tree.file.text[start as usize..end as usize].trim();
    let (facts, from) = match (decl, constant) {
        (
            ast::Decl::Const {
                name,
                value: Some(value),
                ..
            },
            Some(ConstantText { ty, value: checked }),
        ) => {
            let mut facts = written(span.start).to_string(); // the attributes
            if !facts.is_empty() {
                facts.push('\n');
            }
            _ = write!(facts, "const {}: {ty} = {checked}", name.text(&tree.file));
            let initializer = tree.file.slice(tree.exprs.span(*value));
            (facts, (initializer != checked).then_some(initializer))
        }
        (
            ast::Decl::Function(ast::Function {
                body: Some(body), ..
            }),
            _,
        ) => (written(tree.blocks.span(*body).start).to_string(), None),
        _ => (written(span.end).to_string(), None),
    };
    let declared = decl.name().span;
    hover_text(
        &tree.file,
        module,
        declared,
        &facts,
        from,
        metadata.doc_comment,
    )
}
/// What hover shows, in Markdown: `facts`, what the compiler knows of a name,
/// as a Peeper block, ending with the text they were worked out `from`, as an
/// expansion shows its macro; then where the name `declared` is, in the module
/// `module` read from `file`, and its doc comment `doc_comment` as prose.
pub(crate) fn hover_text(
    file: &SourceFile,
    module: &str,
    declared: Span,
    facts: &str,
    from: Option<&str>,
    doc_comment: Option<Span>,
) -> String {
    let mut hover = String::from("```peeper\n");
    hover.push_str(facts);
    if let Some(from) = from {
        hover.push_str("\n\n// from\n");
        hover.push_str(from);
    }
    hover.push_str("\n```\n\n---\n\n");
    // Not in the block: a highlighter would read a path's `06_x` as a number.
    let line = file.line(declared.start as usize);
    _ = writeln!(hover, "*in* `{module}` *· line {line}*");
    if let Some(doc_comment) = doc_comment {
        hover.push('\n');
        for line in file.slice(doc_comment).lines() {
            let line = line.trim_start();
            let line = line.strip_prefix("///").unwrap_or(line);
            hover.push_str(line.strip_prefix(' ').unwrap_or(line));
            hover.push('\n');
        }
    }
    hover
}
/// What hover shows for a word the language gives, `word`, such as a
/// keyword or a built-in type: the word as a Peeper block, then `doc`,
/// Markdown.
pub(crate) fn doc_hover(word: &str, doc: &str) -> String {
    format!("```peeper\n{word}\n```\n\n---\n\n{doc}\n")
}
/// A hover answer holding `value`, Markdown.
pub(crate) fn markdown_hover(value: String) -> Option<serde_json::Value> {
    let hover = Hover {
        contents: HoverContents::Markup(MarkupContent {
            kind: MarkupKind::Markdown,
            value,
        }),
        range: None,
    };
    serde_json::to_value(hover).ok()
}
