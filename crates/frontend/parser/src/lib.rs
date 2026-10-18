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
use diagnostics::Fix;
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
        file,
        tree: Tree::new(file.clone()),
        tokens,
        pos: 0,
        last_end: 0,
        lex_errors: errors.len(),
        errors,
        recovering: false,
        scratch: Vec::new(),
    };
    p.module();
    (p.tree, p.errors)
}

/// Runs `rule` on a new stack segment when less than the red zone is left.
fn grow<R>(rule: impl FnOnce() -> R) -> R {
    const RED_ZONE: usize = 128 * 1024;
    const SEGMENT: usize = 1024 * 1024;
    stacker::maybe_grow(RED_ZONE, SEGMENT, rule)
}

struct Parser<'a> {
    file: &'a Arc<SourceFile>,
    tree: Tree, // the tree being built
    tokens: Vec<Token>,
    pos: usize,
    last_end: u32, // end of the last token read
    errors: Vec<Diagnostic>,
    lex_errors: usize,        // errors[..lex_errors] are the lexer's
    recovering: bool,         // an error was reported and the parser is not yet in step again
    scratch: Vec<NonZeroU32>, // the items of the lists being read, innermost last
}

impl Parser<'_> {
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
        self.file.line(offset as usize)
    }

    fn text(&self, t: Token) -> &str {
        self.file.slice(t.span)
    }

    fn fix(&self, span: Span, new_text: &str) -> Fix {
        Fix {
            file: self.file.clone(),
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

    /// Describes `kind` missing before the current token. A missing closer
    /// belongs at the end of the text read so far, and so does any token when
    /// the current one is on a later line: the error points there and, when
    /// `kind` has one fixed spelling, offers to insert it. Otherwise it points at
    /// the token found. The line decides only where the error points, never how
    /// parsing goes on.
    fn missing(&self, kind: TokenKind) -> Diagnostic {
        let t = self.tok();
        let d = Diagnostic::new(
            diagnostics::EXPECTED_TOKEN,
            format!("expected `{}`, found `{}`", kind.text(), t.kind.text()),
        );
        let closer = matches!(
            kind,
            TokenKind::RightParen
                | TokenKind::RightBracket
                | TokenKind::RightBrace
                | TokenKind::Greater
        );
        if self.last_end == 0 || !closer && self.line(t.span.start) == self.line(self.last_end) {
            return d.label(
                self.file,
                t.span,
                format!("expected `{}` here", kind.text()),
            );
        }
        let d = d.label(self.file, self.here(), "");
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
            let t = self.tok();
            self.fail(
                Diagnostic::new(
                    diagnostics::EXPECTED_TOKEN,
                    format!("expected `{}`, found `{}`", kind.text(), t.kind.text()),
                )
                .label(self.file, t.span, "this cannot be here"),
            );
            while self.current() < i {
                self.next();
            }
            self.next();
            return;
        }
        let mut d = self.missing(kind);
        if self.line(open.span.start) < self.line(d.labels[0].span.start) {
            d = d.label(
                self.file,
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
                self.file,
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
            .label(self.file, semicolons[0].span, "");
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
        let list = self.tree.lists.add(&self.scratch[mark..]);
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
    /// again; when it is missing, it skips to where parsing can go on.
    fn end(&mut self) {
        if self.at(TokenKind::Semicolon) {
            self.next();
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
mod tests {
    use super::*;

    pub(crate) fn parse_text(text: &str) -> (Tree, Vec<Diagnostic>) {
        parse(&Arc::new(SourceFile::new("t.peep", text)))
    }

    /// The tree of `text` in the dump format.
    pub(crate) fn dump_of(text: &str) -> String {
        dump::dump(&parse_text(text).0, false)
    }

    /// The error messages for `text`.
    pub(crate) fn messages(text: &str) -> Vec<String> {
        parse_text(text).1.into_iter().map(|e| e.message).collect()
    }

    /// `text` with the fixes of the first help of `d` applied.
    pub(crate) fn fixed(text: &str, d: &Diagnostic) -> String {
        let mut text = text.to_string();
        for fix in d.notes[0].fixes.iter().rev() {
            text.replace_range(fix.span.range(), &fix.new_text);
        }
        text
    }

    /// Checks that `text` gives exactly one error, `message`, and that its fix
    /// (when `want_fixed` is given) gives `want_fixed`.
    pub(crate) fn one_error(text: &str, message: &str, want_fixed: Option<&str>) {
        let (_, errors) = parse_text(text);
        let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
        assert_eq!(got, [message], "{text:?}");
        if let Some(want) = want_fixed {
            assert_eq!(fixed(text, &errors[0]), want, "{text:?}");
        }
    }

    pub(crate) const PROGRAM: &str = r#"import "app/util" as u;
import "core:io";

fn add(a: i32, b: i32) -> i32 {
    let sum: i32 = a + b * 2;
    return sum;
}

fn main() {
    let x = add(1, (2 + 3)) == 7;
    log("hi");
    return;
}
"#;

    #[test]
    fn parses_a_program() {
        let want = r#"module
  import declaration
    string literal "app/util"
    declared name u
  import declaration
    string literal "core:io"
  function declaration
    declared name add
    parameter
      declared name a
      type name i32
    parameter
      declared name b
      type name i32
    type name i32
    block
      let statement
        declared name sum
        type name i32
        binary expression +
          name reference a
          binary expression *
            name reference b
            number literal 2
      return statement
        name reference sum
  function declaration
    declared name main
    block
      let statement
        declared name x
        binary expression ==
          call expression
            name reference add
            number literal 1
            parentheses
              binary expression +
                number literal 2
                number literal 3
          number literal 7
      expression statement
        call expression
          name reference log
          string literal "hi"
      return statement return;
"#;
        assert!(messages(PROGRAM).is_empty());
        assert_eq!(dump_of(PROGRAM), want);
    }

    #[test]
    fn recovers_after_a_missing_semicolon() {
        let text = "fn f() { let a = 1 let b = 2; return b; }";
        let errors = messages(text);
        assert!(
            errors.len() == 1 && errors[0].contains("expected `;`"),
            "{errors:?}"
        );
        assert!(dump_of(text).contains("return statement\n        name reference b\n"));
    }

    #[test]
    fn one_error_for_unexpected_top_level_text() {
        let text = "let b = 1;\nx + 1; y();\nfn f() {}";
        assert_eq!(
            messages(text),
            [
                "top-level `let` not allowed",
                "module scope expects declaration"
            ]
        );
        assert_eq!(
            dump_of(text),
            "module\n  function declaration\n    declared name f\n    block {}\n"
        );
    }

    /// Inputs with errors in many places; parsing them must still give a tree
    /// that keeps its rules.
    const BROKEN_INPUTS: &[&str] = &[
        "",
        "fn",
        "fn f(",
        "fn f(a: ) {",
        "fn f() { let = ; }",
        "fn f() { return 1 + ; }",
        "fn f() { g(1, ; }",
        "import ;",
        "import \"x\" as ;",
        "let x = 1;",
        "fn f() -> { }",
        "fn f() { ((1 }",
        "fn f( {",
        "fn f() { a$b; }",
        "fn f() { g(1 2); }",
        "fn f(a: i32 b: i32) {}",
        "fn f() { fn }",
        "fn f() { ) ; let x = ; }",
        "fn f() { let a = 1\nfn g() {}",
        "fn f() { g(a b c); h(1 2); }",
        "fn (",
        "fn (a: &) f",
        "fn f<",
        "fn f<T",
        "fn f(a: Box<T) {}",
        "fn f(a: Box<Box<T>) {}",
        "fn f(a: [",
        "fn f(a: [4",
        "fn f(a: fn(",
        "fn f() -> &mut",
        "fn f() -> i32 from (a",
        "fn f(a: ??) {}",
        "fn f(a: a::) {}",
        "fn f(mut mut) {}",
        "fn f() { a.; }",
        "fn f() { a[; }",
        "fn f() { a[1..=]; }",
        "fn f() { a as ; }",
        "fn f() { a is b; }",
        "fn f() { a orelse ; }",
        "fn f() { a |> b; }",
        "fn f() { a |> b.c(); }",
        "fn f() { .{ a = }; }",
        "fn f() { .{ a: 1, b: 2 }; }",
        "fn f() { [_]i32{1, 2; }",
        "fn f() { [x]i32{}; }",
        "fn f() { A::B with ; }",
        "fn f() { Box<T>::; }",
        "fn f() { print(; }",
        "fn f() { &mut ; }",
        "fn f() { a < b > c; }",
        "fn f() { a<b>.{}; }",
        "fn f() { -; }",
        "fn f() { a orelse { return; }; }",
        "fn f() { if }",
        "fn f() { if a }",
        "fn f() { if a {} else }",
        "fn f() { if a = b {} }",
        "fn f() { for }",
        "fn f() { for x in }",
        "fn f() { for i, in x {} }",
        "fn f() { for a.b in x {} }",
        "fn f() { break l; }",
        "fn f() { match }",
        "fn f() { match x { }",
        "fn f() { match x { A::B {} } }",
        "fn f() { match x { A::B => {}, _ => {} } }",
        "fn f() { match x { let } }",
        "fn f() { match x { A::B with => {} } }",
        "fn f() { let .{ a = b, _, c } = v; }",
        "fn f() { let .{ } }",
        "fn f() { a = ; }",
        "fn f() { let mut ; }",
        "fn f() { const c = 1; { } }",
        "///",
        "/// x",
        "#",
        "#[",
        "#[a(",
        "#[a(1, ] fn f();",
        "const",
        "const a",
        "const a: = ;",
        "struct",
        "struct S {",
        "struct S { a: }",
        "enum E { A: }",
        "iface I { fn",
        "iface I { fn (",
        "type",
        "type A =",
        "type A<T",
        "struct S {} struct",
        "fn f() { let a: i32; }",
        "fn f() {\nstruct S {}",
        "/// doc\n#[a] let x = 1;",
    ];

    /// Every span lies in the text and starts before it ends. (A child need not
    /// lie inside its parent: an empty error node sits where reading stopped,
    /// which can be just before the statement that holds it, as in Go.)
    #[test]
    fn spans_lie_in_the_text() {
        for text in std::iter::once(&PROGRAM).chain(BROKEN_INPUTS) {
            let (tree, _) = parse_text(text);
            for line in dump::dump(&tree, true).lines() {
                // the span follows the kind; a doc comment's later lines have none
                let Some(at) = line.find(" @") else { continue };
                let span = line[at + 2..].split(' ').next().unwrap();
                let (start, end) = span.split_once('-').unwrap();
                let (start, end): (usize, usize) = (start.parse().unwrap(), end.parse().unwrap());
                assert!(start <= end && end <= text.len(), "{text:?}: {line}");
            }
        }
    }

    /// Each mistake is reported once, and parsing goes on to report the next.
    #[test]
    fn one_error_per_mistake() {
        for (text, want) in [
            ("fn f( {", &["expected `)`, found `{`"][..]),
            ("fn", &["expected `identifier`, found `end of file`"]),
            ("fn f() { ((1 }", &["expected `)`, found `}`"]),
            ("fn f() { g(1, ; }", &["expected `)`, found `;`"]),
            (
                "fn f() { return 1 + ; }",
                &["expected expression, found `;`"],
            ),
            ("fn f() { g(1 2); }", &["expected `,`"]),
            ("fn f(a: i32 b: i32) {}", &["expected `,`"]),
            ("fn f() { a$b; }", &["illegal character \"$\""]), // the parser adds nothing
            (
                "fn f() { let a = ; let b = ; }",
                &[
                    "expected expression, found `;`",
                    "expected expression, found `;`",
                ],
            ),
            (
                "fn f() { ) ; let x = ; }",
                &[
                    "expected expression, found `)`",
                    "expected expression, found `;`",
                ],
            ),
            (
                "fn f() { g(a b c); h(1 2); }",
                &["expected `,`", "expected `,`"],
            ),
            (
                "fn f() { let a = 1\nfn g() {}",
                &["expected `;`, found `fn`"],
            ),
        ] {
            assert_eq!(messages(text), want, "{text:?}");
        }
    }

    /// Items without commas are read as if the commas were there, with one fix
    /// per missing comma.
    #[test]
    fn missing_commas_are_repaired() {
        let text = "fn f(a: i32 b: i32) { g(a b c); }";
        let want = "module
  function declaration
    declared name f
    parameter
      declared name a
      type name i32
    parameter
      declared name b
      type name i32
    block
      expression statement
        call expression
          name reference g
          name reference a
          name reference b
          name reference c
";
        assert_eq!(dump_of(text), want);
        let (_, errors) = parse_text(text);
        assert_eq!(errors.len(), 2);
        assert_eq!(errors[0].notes[0].fixes.len(), 1);
        assert_eq!(errors[1].notes[0].fixes.len(), 2);
        let second = errors[1].notes[0].fixes[1].span.start as usize;
        assert_eq!(&text[..second], "fn f(a: i32 b: i32) { g(a b");
    }

    /// A missing closer on a later line points back at what is still open.
    #[test]
    fn unclosed_marks_the_opener() {
        let (_, errors) = parse_text("fn f() {\n    g(1;\n}");
        assert!(
            errors.len() == 1 && errors[0].labels.len() == 1,
            "same line: {errors:?}"
        );
        let (_, errors) = parse_text("fn f() {\n    let a = 1;\n");
        assert!(
            errors.len() == 1
                && errors[0].labels.len() == 2
                && errors[0].labels[1].message == "this `{` is still open",
            "later line: {errors:?}"
        );
    }

    /// Where a missing token is reported: at the end of the text read when the
    /// next token is on a later line, with a fix that inserts it there; at the
    /// token found when it is on the same line.
    #[test]
    fn missing_token_place() {
        one_error(
            "fn f() {\n    let a = 1\n    return a;\n}",
            "expected `;`, found `return`",
            Some("fn f() {\n    let a = 1;\n    return a;\n}"),
        );
        let text = "fn f() { let a = 1 let b = 2; }";
        let (_, errors) = parse_text(text);
        assert!(
            errors.len() == 1 && errors[0].notes.is_empty(),
            "{errors:?}"
        );
        assert_eq!(&text[errors[0].labels[0].span.range()], "let");
        // A missing closer always belongs where the read text ends.
        let text = "fn f() { g(1 + 2; }";
        let (_, errors) = parse_text(text);
        assert_eq!(errors.len(), 1);
        assert_eq!(
            errors[0].notes[0].fixes[0].span.start as usize,
            text.find(';').unwrap()
        );
        // A closer that comes later means what stands before it does not belong:
        // that is reported once, and the list ends at its own closer.
        let (tree, errors) = parse_text("fn f() { let p = Point.{ .x = 1, y = 2 }; }\nfn g() {}");
        let got: Vec<_> = errors.iter().map(|e| e.message.as_str()).collect();
        assert_eq!(got, ["expected `}`, found `.`"]);
        assert_eq!(tree.declarations.len(), 2);
    }

    /// What the parser accepts and repairs between items: extra commas,
    /// trailing commas, empty statements and `??` are style, left to a lint; a
    /// `;` for a `,` in a `{ }` list is repaired with one error per list.
    #[test]
    fn separators() {
        for text in [
            "fn f(a: i32,, b: i32,) { g(1,, 2,); ;; }",
            "struct S { a: i32, b: i32, }",
            "enum E { A,, B, }",
            "fn f(a: ??i32) { let p = .{ a = 1,, }; }",
        ] {
            assert!(messages(text).is_empty(), "{text:?}");
        }
        for (text, want) in [
            ("struct S { a: i32; b: i32 }", "struct S { a: i32, b: i32 }"),
            (
                "struct S { a: i32; b: i32; }",
                "struct S { a: i32, b: i32 }",
            ),
            (
                "fn f() { let p = .{ a = 1; b = 2 }; }",
                "fn f() { let p = .{ a = 1, b = 2 }; }",
            ),
            (
                "fn f() { let v = [2]i32{1; 2}; }",
                "fn f() { let v = [2]i32{1, 2}; }",
            ),
        ] {
            one_error(text, "expected `,` between items, found `;`", Some(want));
        }
        // A `;` after an unclosed literal ends the statement: the `}` is missing.
        one_error(
            "fn f() {\n    let v = [3]i32{4, 5, 6;\n    return;\n}",
            "expected `}`, found `;`",
            None,
        );
        // Only before `]` may a range leave out its end.
        one_error(
            "fn f() { for i in 0.. { } }",
            "expected expression, found `{`",
            None,
        );
    }

    /// Nesting far deeper than the thread's stack would hold still parses:
    /// the nesting rules move to new stack segments as they need them.
    #[test]
    fn deep_nesting_does_not_overflow() {
        let n = 100_000;
        for text in [
            format!("fn f() {{ let a = {}1{}; }}", "(".repeat(n), ")".repeat(n)),
            format!("fn f() {}{}", "{".repeat(n), "}".repeat(n)),
            format!("fn f() {{ if a {{ }}{} }}", " else if a { }".repeat(n)),
            format!("fn f(a: {}i32) {{}}", "& ".repeat(n)),
            format!(
                "fn f() {{ match x {{ {}c => {{}} }} }}",
                "A::B with ".repeat(n)
            ),
            format!("fn f() {{ let a = b{}", " orelse { c".repeat(n)),
        ] {
            let (tree, _) = parse_text(&text);
            assert_eq!(tree.declarations.len(), 1);
        }
    }
}
