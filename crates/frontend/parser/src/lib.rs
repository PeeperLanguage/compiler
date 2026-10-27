//! Builds the syntax tree of one file: imports; functions, constants, structs,
//! enums, interfaces and type aliases with their doc comments and attributes;
//! and every type, statement, pattern and expression.
//!
//! A node's span runs from the token that begins it to the end of the last
//! token read for it. A missing name or expression is an empty span there.
//!
//! One mistake gives one error. After an error the parser is recovering: it
//! reports nothing more until it is in step with the source again, at the next
//! `;`, the `}` closing the block, or a keyword starting a statement or
//! declaration (see `sync`).
//!
//! The parser is recursive. The rules that nest (`expr`, `type_expr`, `stmt`,
//! `if_statement`, `case_pattern`) run inside `grow`, which moves to a new
//! stack segment when little stack is left, so no nesting depth overflows the
//! stack.

mod decl;
mod expr;
mod literals;
mod pattern;
mod stmt;
mod types;

#[cfg(test)]
mod dump;

use std::num::NonZeroU32;
use std::sync::Arc;

use arena::Id;
use arena::List;
use arena::Node;
use diagnostics::Diagnostic;
use diagnostics::fix::Fix;
use model::ast::Ident;
use model::ast::Tree;
use source::SourceFile;
use source::Span;
use token::Token;
use token::TokenKind;

/// Returns the syntax tree of `file` and the lexical and syntax errors in it.
pub fn parse(file: &Arc<SourceFile>) -> (Tree, Vec<Diagnostic>) {
    let (tokens, errors) = lexer::scan(file);
    let mut p = Parser {
        tree: Tree::new(file.clone()),
        tokens,
        pos: 0,
        last_end: 0,
        lex_errors: errors.len(),
        errors,
        recovering: false,
        scratch: Vec::new(),
    };
    p.tree.complete = p.lex_errors == 0;
    p.module();
    (p.tree, p.errors)
}

/// Runs `rule` on a new stack segment when less than the red zone is left.
fn grow<R>(rule: impl FnOnce() -> R) -> R {
    const RED_ZONE: usize = 128 * 1024;
    const SEGMENT: usize = 1024 * 1024;
    stacker::maybe_grow(RED_ZONE, SEGMENT, rule)
}

struct Parser {
    tree: Tree, // the tree being built
    tokens: Vec<Token>,
    pos: usize,
    last_end: u32, // end of the last token read
    errors: Vec<Diagnostic>,
    lex_errors: usize,        // errors[..lex_errors] are the lexer's
    recovering: bool,         // an error was reported and the parser is not yet in step again
    scratch: Vec<NonZeroU32>, // the items of the lists being read, innermost last
}

impl Parser {
    /// The index of the current token: the next one that is not a doc comment.
    /// Doc comments are read only where a declaration may begin (see
    /// `doc_comment`); everywhere else the parser looks past them.
    fn current(&self) -> usize {
        let mut i = self.pos;
        while self.tokens[i].kind == TokenKind::DocComment {
            i += 1;
        }
        i
    }

    fn tok(&self) -> Token {
        self.tokens[self.current()]
    }

    fn at(&self, kind: TokenKind) -> bool {
        self.tok().kind == kind
    }

    /// Whether the current token is `yes`; it must be `yes` or `no`, as for a
    /// rule registered for those two kinds only.
    fn is_either(&self, yes: TokenKind, no: TokenKind) -> bool {
        match self.tok().kind {
            kind if kind == yes => true,
            kind if kind == no => false,
            kind => unreachable!(
                "expected `{}` or `{}`, found `{}`",
                yes.text(),
                no.text(),
                kind.text()
            ),
        }
    }

    /// The token `n` tokens after the current one, or the end of file past the
    /// end.
    fn ahead(&self, mut n: usize) -> Token {
        let mut i = self.current();
        while n > 0 && self.tokens[i].kind != TokenKind::Eof {
            i += 1;
            while self.tokens[i].kind == TokenKind::DocComment {
                i += 1;
            }
            n -= 1;
        }
        self.tokens[i]
    }

    fn next(&mut self) -> Token {
        self.pos = self.current();
        let t = self.tokens[self.pos];
        if t.kind != TokenKind::Eof {
            self.pos += 1;
            self.last_end = t.span.end;
        }
        t
    }

    /// An empty span at the end of what was read.
    fn here(&self) -> Span {
        Span::at(self.last_end as usize)
    }

    fn line(&self, offset: u32) -> usize {
        self.tree.file.line(offset as usize)
    }

    fn text(&self, t: Token) -> &str {
        self.tree.file.slice(t.span)
    }

    fn fix(&self, span: Span, new_text: &str) -> Fix {
        Fix {
            file: self.tree.file.clone(),
            span,
            new_text: new_text.to_string(),
        }
    }

    /// Adds `node`, spanning from `start`, where the token beginning it was, to
    /// the end of the last token read. A node that read nothing (its start lies
    /// past what was read) is an empty span at the read position.
    fn add<T: Node<Container = Tree>>(&mut self, start: u32, node: T) -> Id<T> {
        let span = Span::new(start.min(self.last_end) as usize, self.last_end as usize);
        self.tree.add(node, span)
    }

    /// Reports a mistake the parser cannot read past, and starts recovering.
    /// Nothing is reported while recovering already, or when a lexical error lies
    /// between the last token read and the current one: an illegal character
    /// makes no token, so the parser's error would only repeat it.
    fn fail(&mut self, d: Diagnostic) {
        if !self.recovering && !self.after_lexical_error() {
            self.errors.push(d);
        }
        self.recovering = true;
        self.tree.complete = false; // `sync` skips what follows
    }

    /// Reports a mistake the parser reads past as if the missing token were
    /// there, so it stays in step and goes on reporting.
    fn repair(&mut self, d: Diagnostic) {
        if !self.recovering {
            self.errors.push(d);
        }
    }

    fn after_lexical_error(&self) -> bool {
        let next = self.tok().span.start;
        self.errors[..self.lex_errors].iter().any(|e| {
            let span = e.labels[0].span;
            self.last_end <= span.start && span.end <= next
        })
    }

    fn expect(&mut self, kind: TokenKind) -> Option<Token> {
        if self.at(kind) {
            return Some(self.next());
        }
        let d = self.missing(kind);
        self.fail(d);
        None
    }

    /// "expected `kind`, found" the current token, without a label.
    fn expected(&self, kind: TokenKind) -> Diagnostic {
        Diagnostic::new(
            diagnostics::EXPECTED_TOKEN,
            format!(
                "expected `{}`, found `{}`",
                kind.text(),
                self.tok().kind.text()
            ),
        )
    }

    /// Describes `kind` missing before the current token. A missing closer
    /// belongs at the end of the text read so far, and so does any token when
    /// the current one is on a later line: the error points there and, when
    /// `kind` has one fixed spelling, offers to insert it. Otherwise it points at
    /// the token found. The line decides only where the error points, never how
    /// parsing goes on.
    fn missing(&self, kind: TokenKind) -> Diagnostic {
        let t = self.tok();
        let d = self.expected(kind);
        let closer = matches!(
            kind,
            TokenKind::RightParen
                | TokenKind::RightBracket
                | TokenKind::RightBrace
                | TokenKind::Greater
        );
        if self.last_end == 0 || !closer && self.line(t.span.start) == self.line(self.last_end) {
            return d.label(
                &self.tree.file,
                t.span,
                format!("expected `{}` here", kind.text()),
            );
        }
        let d = d.label(&self.tree.file, self.here(), "");
        // only a keyword or an operator has one text to insert
        if !token::KEYWORDS.contains(&kind) && !token::OPERATORS.contains(&kind) {
            return d;
        }
        let text = kind.text();
        d.help(
            format!("add missing `{text}`"),
            vec![self.fix(self.here(), text)],
        )
    }

    /// Reads the token closing what `open` opened. When that closer comes later
    /// at this depth, what stands before it cannot be in the list: that is
    /// reported, and parsing skips to the closer. Otherwise the closer is
    /// missing, and the error also marks `open` if it is on an earlier line.
    fn close(&mut self, kind: TokenKind, open: Token) {
        if self.at(kind) {
            self.next();
            return;
        }
        let i = self.closer_ahead();
        if self.tokens[i].kind == kind {
            let d = self.expected(kind);
            self.fail(d.label(&self.tree.file, self.tok().span, "this cannot be here"));
            while self.current() < i {
                self.next();
            }
            self.next();
            return;
        }
        let mut d = self.missing(kind);
        if self.line(open.span.start) < self.line(d.labels[0].span.start) {
            d = d.label(
                &self.tree.file,
                open.span,
                format!("this `{}` is still open", open.kind.text()),
            );
        }
        self.fail(d);
    }

    /// Skips to where parsing can go on after an error: past the next `;`, or up
    /// to a `}` closing the current block, a keyword that starts a statement, or
    /// the end of file. Brackets opened on the way are skipped whole, and a stray
    /// closing `)` or `]` is skipped. Passing a `;` puts the parser in step again.
    fn sync(&mut self) {
        let mut depth = 0usize;
        while !self.at(TokenKind::Eof) {
            match self.tok().kind {
                TokenKind::LeftParen | TokenKind::LeftBracket | TokenKind::LeftBrace => depth += 1,
                TokenKind::RightParen | TokenKind::RightBracket => depth = depth.saturating_sub(1),
                TokenKind::RightBrace => {
                    if depth == 0 {
                        return;
                    }
                    depth -= 1;
                }
                TokenKind::Semicolon if depth == 0 => {
                    self.next();
                    self.recovering = false;
                    return;
                }
                kind if depth == 0 && starts_statement(kind) => return,
                _ => {}
            }
            self.next();
        }
    }

    /// Reads the items of a list up to `close`, separated by commas. `item`
    /// reads one item and `starts_item` tells whether a token can begin one.
    /// Extra commas are accepted (they are a matter of style, left to a lint).
    /// Two mistakes are read as if the comma were there, and each kind is
    /// reported once per list:
    ///   - an item that follows another without a comma, when the list's own
    ///     close comes next at this depth;
    ///   - a `;` between the items of a `{ }` list: always in a declaration's
    ///     body (`in_declaration`), and in an expression's braces when they end
    ///     the expression (`;`, `,` or `)` follows the `}`).
    fn items<T>(
        &mut self,
        close: TokenKind,
        in_declaration: bool,
        starts_item: fn(TokenKind) -> bool,
        mut item: impl FnMut(&mut Self) -> Id<T>,
    ) -> List<T> {
        let mark = self.scratch.len();
        let mut missing = Vec::new();
        let mut semicolons = Vec::new();
        loop {
            while self.at(TokenKind::Comma) {
                self.next();
            }
            if !starts_item(self.tok().kind) {
                break; // neither the close nor the end of file starts an item
            }
            let id = item(self);
            self.scratch.push(id.into());
            let t = self.tok();
            if t.kind == TokenKind::Comma {
                continue;
            }
            if t.kind == TokenKind::Semicolon
                && close == TokenKind::RightBrace
                && self.semicolon_separates(in_declaration)
            {
                let text = if self.ahead(1).kind == TokenKind::RightBrace {
                    ""
                } else {
                    ","
                };
                semicolons.push(self.fix(t.span, text));
                self.next();
                continue;
            }
            // `>` is no bracket the closer scan follows; in `<T U>` the next name
            // can only be another item.
            if starts_item(t.kind)
                && (close == TokenKind::Greater || self.tokens[self.closer_ahead()].kind == close)
            {
                missing.push(self.fix(self.here(), ","));
                continue;
            }
            break;
        }
        if !missing.is_empty() {
            let help = if missing.len() > 1 {
                "add the missing commas"
            } else {
                "add missing `,`"
            };
            let d = Diagnostic::new(diagnostics::EXPECTED_TOKEN, "expected `,`").label(
                &self.tree.file,
                missing[0].span,
                "",
            );
            self.repair(d.help(help, missing));
        }
        if !semicolons.is_empty() {
            let d = Diagnostic::new(
                diagnostics::EXPECTED_TOKEN,
                "expected `,` between items, found `;`",
            )
            .label(&self.tree.file, semicolons[0].span, "");
            self.repair(d.help("separate items with `,`", semicolons));
        }
        self.finish_list(mark)
    }

    /// Reads `open`, the items of a list up to `close` (see `items`), and
    /// `close`. A missing `open` is reported and gives an empty list.
    fn delimited<T>(
        &mut self,
        open: TokenKind,
        close: TokenKind,
        in_declaration: bool,
        starts_item: fn(TokenKind) -> bool,
        item: impl FnMut(&mut Self) -> Id<T>,
    ) -> List<T> {
        let Some(open) = self.expect(open) else {
            return List::EMPTY;
        };
        let items = self.items(close, in_declaration, starts_item, item);
        self.close(close, open);
        items
    }

    /// Moves the ids that `scratch` holds from `mark` on into a list.
    fn finish_list<T>(&mut self, mark: usize) -> List<T> {
        let list = self.tree.children.lists.add(&self.scratch[mark..]);
        self.scratch.truncate(mark);
        list
    }

    /// Whether the `;` at the current token stands for a `,` in a `{ }` list
    /// (see `items`): the list's `}` comes next at this depth, and the list is a
    /// declaration's body or `;`, `,` or `)` follows the `}`.
    fn semicolon_separates(&self, in_declaration: bool) -> bool {
        let i = self.closer_ahead();
        if self.tokens[i].kind != TokenKind::RightBrace {
            return false;
        }
        in_declaration
            || matches!(
                self.tokens[(i + 1).min(self.tokens.len() - 1)].kind,
                TokenKind::Semicolon | TokenKind::Comma | TokenKind::RightParen
            )
    }

    /// The index of the first closing bracket ahead at the current depth, or of
    /// the end of file when there is none.
    fn closer_ahead(&self) -> usize {
        let mut depth = 0usize;
        for (i, t) in self.tokens.iter().enumerate().skip(self.pos) {
            match t.kind {
                TokenKind::LeftParen | TokenKind::LeftBracket | TokenKind::LeftBrace => depth += 1,
                TokenKind::RightParen | TokenKind::RightBracket | TokenKind::RightBrace => {
                    if depth == 0 {
                        return i;
                    }
                    depth -= 1;
                }
                _ => {}
            }
        }
        self.tokens.len() - 1
    }

    /// Reads a name; a missing one is empty, where it should be.
    fn name(&mut self) -> Ident {
        let span = match self.expect(TokenKind::Ident) {
            Some(t) => t.span,
            None => self.here(),
        };
        Ident { span }
    }

    /// Reads the `;` that closes a statement, which puts the parser in step
    /// again. A `;` missing at the end of a line, before what starts the next
    /// statement or closes the block, is read as if it were there: nothing
    /// needs skipping. Otherwise the parser skips to where parsing can go on.
    fn end(&mut self) {
        if self.at(TokenKind::Semicolon) {
            self.next();
            self.recovering = false;
            return;
        }
        let t = self.tok();
        let next_line = self.line(t.span.start) > self.line(self.last_end);
        let next_starts = matches!(
            t.kind,
            TokenKind::RightBrace | TokenKind::Eof | TokenKind::Ident
        ) || starts_statement(t.kind);
        if next_line && next_starts {
            let d = self.missing(TokenKind::Semicolon);
            self.repair(d);
            self.recovering = false;
            return;
        }
        self.expect(TokenKind::Semicolon);
        self.sync();
    }
}

/// Whether `kind` begins a statement or declaration wherever it appears, so
/// parsing is in step again there.
fn starts_statement(kind: TokenKind) -> bool {
    stmt::is_statement_keyword(kind) || decl::starts_declaration(kind)
}

fn is_name(kind: TokenKind) -> bool {
    kind == TokenKind::Ident
}

#[cfg(test)]
mod tests;
