//! Turns source text into tokens.

use std::sync::Arc;

use diagnostics::{self as diagnostic, Code, Diagnostic, quote};
use literal::{escape, next_char_width};
use source::{SourceFile, Span};
use token::{KEYWORDS, OPERATORS, Token, TokenKind};
use unicode_general_category::{GeneralCategory, get_general_category};

/// Returns the tokens of `file`, ending with an end-of-file token, and the
/// lexical errors in it. An illegal character is reported and produces no
/// token. A malformed or unclosed literal is reported and still produces its
/// token, so parsing goes on without a second error.
pub fn scan(file: &Arc<SourceFile>) -> (Vec<Token>, Vec<Diagnostic>) {
    let mut s = Scanner {
        file,
        text: file.text.as_bytes(),
        pos: 0,
        tokens: Vec::with_capacity(file.text.len() / 3 + 1),
        errors: Vec::new(),
    };
    while s.pos < s.text.len() {
        s.next();
    }
    s.emit(TokenKind::Eof, s.pos);
    (s.tokens, s.errors)
}

struct Scanner<'a> {
    file: &'a Arc<SourceFile>,
    text: &'a [u8],
    pos: usize,
    tokens: Vec<Token>,
    errors: Vec<Diagnostic>,
}

impl Scanner<'_> {
    fn next(&mut self) {
        let start = self.pos;
        match self.text[start] {
            b' ' | b'\t' | b'\n' | b'\r' | b'\x0c' => self.pos += 1,
            b'/' if self.at(1) == b'/' => {
                self.pos = self.line_end();
                if self.text[start..].starts_with(b"///") {
                    self.emit(TokenKind::DocComment, start);
                }
            }
            b'/' if self.at(1) == b'*' => match find(&self.text[start + 2..], b"*/") {
                Some(end) => self.pos = start + 2 + end + 2,
                None => {
                    self.fail(
                        Span::new(start, start + 2),
                        diagnostic::UNTERMINATED_COMMENT,
                        "unterminated block comment",
                        "add `*/` to close it",
                    );
                    self.pos = self.text.len();
                }
            },
            b'"' => self.quoted(0, TokenKind::String),
            b'c' if self.at(1) == b'"' => self.quoted(1, TokenKind::CString),
            b'b' if self.at(1) == b'\'' => self.quoted(1, TokenKind::ByteChar),
            b'\'' => self.quoted(0, TokenKind::Char),
            c if c.is_ascii_digit() => self.number(),
            c if c == b'_' || c.is_ascii_alphabetic() => self.word(),
            c if c.is_ascii() => self.operator(),
            _ => {
                if is_letter(self.char_at(start)) {
                    self.word();
                }
            }
        }
        if self.pos == start {
            self.illegal();
        }
    }

    /// The byte `offset` bytes after the current one, or 0 past the end.
    fn at(&self, offset: usize) -> u8 {
        self.text.get(self.pos + offset).copied().unwrap_or(0)
    }

    /// The character starting at byte `at`, which starts one.
    fn char_at(&self, at: usize) -> char {
        self.file.text[at..].chars().next().unwrap_or('\0')
    }

    fn line_end(&self) -> usize {
        self.text[self.pos..]
            .iter()
            .position(|&c| c == b'\n' || c == b'\r')
            .map_or(self.text.len(), |end| self.pos + end)
    }

    fn emit(&mut self, kind: TokenKind, start: usize) {
        self.tokens.push(Token {
            kind,
            span: Span::new(start, self.pos),
        });
    }

    fn fail(&mut self, span: Span, code: Code, message: impl Into<String>, label: &str) {
        self.errors
            .push(Diagnostic::new(code, message).label(self.file, span, label));
    }

    /// Reads a name or keyword. A name holding letters, digits or marks outside
    /// ASCII is reported once and still read as a name, so parsing goes on.
    fn word(&mut self) {
        let start = self.pos;
        let mut ascii = true;
        while let Some(&c) = self.text.get(self.pos) {
            if c.is_ascii() {
                if c != b'_' && !c.is_ascii_alphanumeric() {
                    break;
                }
                self.pos += 1;
                continue;
            }
            let c = self.char_at(self.pos);
            if !is_letter(c) && (self.pos == start || !is_digit(c) && !is_mark(c)) {
                break;
            }
            ascii = false;
            self.pos += c.len_utf8();
        }
        let text = &self.file.text[start..self.pos];
        if ascii {
            self.emit(keyword(text).unwrap_or(TokenKind::Ident), start);
            return;
        }
        let message = format!("name `{text}` uses characters that are not allowed in names");
        self.fail(
            Span::new(start, self.pos),
            diagnostic::UNEXPECTED_CHARACTER,
            message,
            "use only `A-Z`, `a-z`, `0-9` and `_`",
        );
        self.emit(TokenKind::Ident, start);
    }

    /// Reads a number literal: 0x, 0o and 0b numbers take letters and digits as
    /// digits (checked here against the base); a decimal number may have a
    /// fraction, an exponent and a type suffix such as i32 or f64. A single `_`
    /// may sit between digits. The suffix is the parser's to check.
    fn number(&mut self) {
        let start = self.pos;
        if self.text[start] == b'0'
            && b"xXoObB".contains(&self.at(1))
            && self.at(2).is_ascii_alphanumeric()
        {
            self.pos += 2;
            self.digits(u8::is_ascii_alphanumeric);
            self.emit(TokenKind::Number, start);
            let literal = numeric::split(&self.file.text[start..self.pos]);
            if !literal.digits_in_base() {
                let base = match literal.base {
                    16 => "hexadecimal",
                    8 => "octal",
                    _ => "binary",
                };
                let prefixed = &self.file.text[start..start + 2 + literal.digits.len()]; // without the suffix
                let message = format!("invalid {base} literal {prefixed}");
                self.fail(
                    Span::new(start, self.pos),
                    diagnostic::INVALID_NUMBER,
                    message,
                    "",
                );
            }
            return;
        }
        self.digits(u8::is_ascii_digit);
        if self.at(0) == b'.' && self.at(1).is_ascii_digit() {
            self.pos += 1;
            self.digits(u8::is_ascii_digit);
        }
        if matches!(self.at(0), b'e' | b'E') {
            let sign = usize::from(matches!(self.at(1), b'+' | b'-'));
            if self.at(1 + sign).is_ascii_digit() {
                self.pos += 1 + sign;
                self.digits(u8::is_ascii_digit);
            }
        }
        if matches!(self.at(0), b'i' | b'u' | b'f') {
            self.pos += 1;
            while self.at(0).is_ascii_digit() || self.at(0) == b'_' {
                self.pos += 1;
            }
        }
        self.emit(TokenKind::Number, start);
    }

    /// Reads one or more digits; `_` is taken only when a digit follows it.
    fn digits(&mut self, is_digit: fn(&u8) -> bool) {
        loop {
            if is_digit(&self.at(0)) {
                self.pos += 1;
            } else if self.at(0) == b'_' && is_digit(&self.at(1)) {
                self.pos += 2;
            } else {
                return;
            }
        }
    }

    /// Reads a string, character or byte literal whose quote follows `prefix`
    /// bytes, in one pass: it finds the closing quote, reports each bad escape
    /// at the escape, and checks that a character or byte literal holds exactly
    /// one. A literal may span lines; an escape needs a following character on
    /// the same line. When the closing quote is missing, the literal is
    /// reported and ends with its first line. The literal's token is kept even
    /// when it is invalid, so parsing goes on without a second error.
    fn quoted(&mut self, prefix: usize, kind: TokenKind) {
        let start = self.pos;
        let quote_char = self.text[start + prefix];
        let mut value = [0u8; 4]; // the first bytes of the literal's value
        let (mut length, mut bad_escape) = (0, false);
        let mut i = start + prefix + 1;
        loop {
            let unclosed = match self.text.get(i) {
                None => true,
                Some(b'\\') => matches!(self.text.get(i + 1), None | Some(b'\n')),
                Some(_) => false,
            };
            if unclosed {
                self.unterminated(start, quote_char, kind);
                return;
            }
            let c = self.text[i];
            if c == quote_char {
                self.pos = i + 1;
                if !bad_escape {
                    self.check_length(start, kind, &value[..length.min(4)], length);
                }
                self.emit(kind, start);
                return;
            }
            let (byte, width) = if c == b'\\' {
                let (byte, width) = escape(self.text, i, quote_char);
                match byte {
                    Ok(byte) => (byte, width),
                    Err(message) => {
                        self.fail(
                            Span::new(i, i + width),
                            diagnostic::UNEXPECTED_CHARACTER,
                            message,
                            "fix this escape sequence",
                        );
                        bad_escape = true;
                        (0, width)
                    }
                }
            } else {
                (c, 1)
            };
            if length < value.len() {
                value[length] = byte;
            }
            length += 1;
            i += width;
        }
    }

    /// Reports a character literal whose value is not exactly one valid UTF-8
    /// character, or a byte literal whose value is not exactly one byte. `value`
    /// holds the first bytes of the literal's value, which is `length` bytes long.
    fn check_length(&mut self, start: usize, kind: TokenKind, value: &[u8], length: usize) {
        let span = Span::new(start, self.pos);
        let one_char = || std::str::from_utf8(value).is_ok_and(|v| v.chars().count() == 1);
        if kind == TokenKind::Char && (length > 4 || !one_char()) {
            self.fail(
                span,
                diagnostic::UNEXPECTED_CHARACTER,
                "character literal must contain exactly one character",
                "use exactly one valid UTF-8 character between single quotes",
            );
        } else if kind == TokenKind::ByteChar && length != 1 {
            self.fail(
                span,
                diagnostic::UNEXPECTED_CHARACTER,
                "byte literal must contain exactly one byte",
                "use exactly one byte after the b'...' prefix",
            );
        }
    }

    /// Reports a literal without its closing quote and emits it up to the end of
    /// its first line.
    fn unterminated(&mut self, start: usize, quote_char: u8, kind: TokenKind) {
        self.pos = self.line_end();
        let span = Span::new(start, self.pos);
        if quote_char == b'"' {
            self.fail(
                span,
                diagnostic::UNTERMINATED_STRING,
                "unterminated string literal",
                "add a closing `\"`",
            );
        } else {
            self.fail(
                span,
                diagnostic::UNTERMINATED_STRING,
                "unterminated character literal",
                "add a closing `'`",
            );
        }
        self.emit(kind, start);
    }

    fn illegal(&mut self) {
        let start = self.pos;
        self.pos += next_char_width(&self.text[start..]);
        let message = format!("illegal character {}", quote(&self.text[start..self.pos]));
        self.fail(
            Span::new(start, self.pos),
            diagnostic::UNEXPECTED_CHARACTER,
            message,
            "remove or replace this character",
        );
    }

    /// Reads an operator, or reads nothing when none starts here.
    fn operator(&mut self) {
        let rest = &self.text[self.pos..];
        let (first, count) = OPERATORS_BY_FIRST[rest[0] as usize];
        let group = &OPERATORS[first as usize..(first + count) as usize];
        if let Some(&kind) = group
            .iter()
            .find(|kind| rest.starts_with(kind.text().as_bytes()))
        {
            let start = self.pos;
            self.pos += kind.text().len();
            self.emit(kind, start);
        }
    }
}

/// For each ASCII byte, where the kinds of `kinds` whose text starts with it
/// begin and how many there are. `kinds` lists each leading byte's kinds
/// together, which the build checks.
const fn by_first_byte(kinds: &[TokenKind]) -> [(u8, u8); 128] {
    let mut table = [(0u8, 0u8); 128];
    let mut i = 0;
    while i < kinds.len() {
        let first = kinds[i].text().as_bytes()[0] as usize;
        assert!(
            table[first].1 == 0,
            "each leading byte's kinds must be listed together"
        );
        let start = i;
        while i < kinds.len() && kinds[i].text().as_bytes()[0] as usize == first {
            i += 1;
        }
        table[first] = (start as u8, (i - start) as u8);
    }
    table
}

const OPERATORS_BY_FIRST: [(u8, u8); 128] = by_first_byte(OPERATORS);
const KEYWORDS_BY_FIRST: [(u8, u8); 128] = by_first_byte(KEYWORDS);

/// The keyword kind of the ASCII name `text`, or None for a name.
fn keyword(text: &str) -> Option<TokenKind> {
    let (first, count) = KEYWORDS_BY_FIRST[text.as_bytes()[0] as usize];
    KEYWORDS[first as usize..(first + count) as usize]
        .iter()
        .copied()
        .find(|kind| kind.text() == text)
}

fn is_letter(c: char) -> bool {
    matches!(
        get_general_category(c),
        GeneralCategory::UppercaseLetter
            | GeneralCategory::LowercaseLetter
            | GeneralCategory::TitlecaseLetter
            | GeneralCategory::ModifierLetter
            | GeneralCategory::OtherLetter
    )
}

fn is_digit(c: char) -> bool {
    get_general_category(c) == GeneralCategory::DecimalNumber
}

fn is_mark(c: char) -> bool {
    matches!(
        get_general_category(c),
        GeneralCategory::NonspacingMark
            | GeneralCategory::SpacingMark
            | GeneralCategory::EnclosingMark
    )
}

fn find(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keywords() {
        for &kind in KEYWORDS {
            assert_eq!(keyword(kind.text()), Some(kind));
        }
        for name in ["name", "f", "fns", "_", "while"] {
            assert_eq!(keyword(name), None, "{name}");
        }
    }

    /// The tokens of `text` as "kind:text" (end of file left out) and the error
    /// messages.
    fn scan_text(text: &str) -> (String, Vec<String>) {
        let file = Arc::new(SourceFile::new("test.peep", text));
        let (tokens, errors) = scan(&file);
        let parts: Vec<String> = tokens[..tokens.len() - 1]
            .iter()
            .map(|t| format!("{}:{}", t.kind.text(), file.slice(t.span)))
            .collect();
        (
            parts.join(" "),
            errors.into_iter().map(|e| e.message).collect(),
        )
    }

    #[test]
    fn tokens_and_errors() {
        for (name, text, tokens, errors) in [
            (
                "declaration",
                "let mut y: i32 = x;",
                "let:let mut:mut identifier:y ::: identifier:i32 =:= identifier:x ;:;",
                &[][..],
            ),
            (
                "comments",
                "/// doc\n//// doc too\n// note\n/* block */ x",
                "document comment:/// doc document comment://// doc too identifier:x",
                &[],
            ),
            (
                "longest operators first",
                "<<= ||> |> ..= ... :: -> !== ??",
                "<<:<< =:= ||:|| >:> |>:|> ..=:..= ...:... ::::: ->:-> !=:!= =:= ??:??",
                &[],
            ),
            (
                "literals",
                r#""s" c"c" 'x' b'y' 42"#,
                r#"string:"s" c string:c"c" character:'x' byte character:b'y' number:42"#,
                &[],
            ),
            (
                "numbers",
                "0x1F_ff 0b1 1_000 1.5 1..2 1e5 1e 1i32 0x",
                "number:0x1F_ff number:0b1 number:1_000 number:1.5 number:1 ..:.. number:2 number:1e5 number:1 identifier:e number:1i32 number:0 identifier:x",
                &[],
            ),
            (
                "unclosed block comment",
                "x /* y",
                "identifier:x",
                &["unterminated block comment"],
            ),
            (
                "string over two lines",
                "\"a\nb\" x",
                "string:\"a\nb\" identifier:x",
                &[],
            ),
            (
                "unclosed string ends with its line",
                "\"open\nx",
                r#"string:"open identifier:x"#,
                &["unterminated string literal"],
            ),
            (
                "unclosed c string",
                r#"c"open"#,
                r#"c string:c"open"#,
                &["unterminated string literal"],
            ),
            (
                "unclosed character",
                "b'x\ny",
                "byte character:b'x identifier:y",
                &["unterminated character literal"],
            ),
            (
                "bad escape keeps its token",
                r#""a\q" x"#,
                r#"string:"a\q" identifier:x"#,
                &[r#"unknown escape sequence "\\q""#],
            ),
            (
                "character count keeps the tokens",
                "'ab' b'é'",
                "character:'ab' byte character:b'é'",
                &[
                    "character literal must contain exactly one character",
                    "byte literal must contain exactly one byte",
                ],
            ),
            (
                "non-ASCII name",
                "café x",
                "identifier:café identifier:x",
                &["name `café` uses characters that are not allowed in names"],
            ),
            // A combining mark continues a name; a letter number (ⅰ) starts none.
            (
                "combining mark",
                "e\u{301}x",
                "identifier:e\u{301}x",
                &["name `e\u{301}x` uses characters that are not allowed in names"],
            ),
            (
                "letter number",
                "ⅰ1",
                "number:1",
                &["illegal character \"ⅰ\""],
            ),
            (
                "illegal character",
                "a $ b",
                "identifier:a identifier:b",
                &[r#"illegal character "$""#],
            ),
        ] {
            let (got_tokens, got_errors) = scan_text(text);
            assert_eq!(got_tokens, tokens, "{name}: tokens");
            assert_eq!(got_errors, errors, "{name}: errors");
        }
    }

    #[test]
    fn escape_errors_point_at_the_escape() {
        let text = r#""a\q b\xZZ c" 'é' '\xC3\xA9' 'ab' b'\x41'"#;
        let file = Arc::new(SourceFile::new("t.peep", text));
        let (tokens, errors) = scan(&file);
        let got: Vec<String> = errors
            .iter()
            .map(|e| format!("{}: {}", file.slice(e.labels[0].span), e.message))
            .collect();
        assert_eq!(
            got,
            [
                r#"\q: unknown escape sequence "\\q""#,
                r#"\xZZ: invalid hex escape "\\xZZ""#,
                "'ab': character literal must contain exactly one character",
            ]
        );
        assert_eq!(tokens.len(), 6); // five literals and the end of file
    }

    #[test]
    fn number_digits() {
        for (text, message) in [
            ("0xFG", "invalid hexadecimal literal 0xFG"),
            ("0o8", "invalid octal literal 0o8"),
            ("0b102u8", "invalid binary literal 0b102"),
            ("0xu8", "invalid hexadecimal literal 0x"),
            ("0", ""),
            ("0x1f_u8", ""),
            ("0XAB", ""),
            ("1i", ""),
            ("007", ""),
        ] {
            let file = Arc::new(SourceFile::new("t.peep", text));
            let (tokens, errors) = scan(&file);
            assert_eq!(tokens.len(), 2, "{text}: one number token");
            assert_eq!(
                errors.first().map_or("", |e| e.message.as_str()),
                message,
                "{text}"
            );
            assert!(
                errors.len() <= 1 && errors.iter().all(|e| e.labels[0].span == tokens[0].span),
                "{text}"
            );
        }
    }
}
