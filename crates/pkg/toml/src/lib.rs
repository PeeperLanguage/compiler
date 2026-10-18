//! Deprecated: peeper reads and writes TOML with `toml_edit`, which follows the
//! whole TOML spec and keeps a file's layout when it edits. This crate is kept,
//! unused, until the owner decides to delete it.
//!
//! Reads and writes the small part of TOML that project files use, one line
//! at a time: `[section]` headers, `key = value` pairs, and `#` comments. A
//! value is a string in double quotes, an integer, a float, `true` or `false`,
//! an array `[a, b]` or an inline table `{ key = value }`, which may go on over
//! later lines and hold comments there; any other value is an error. Keys before the first header are in the
//! section "default". A `Document` changes a file in place: setting a value
//! rewrites only that value's text, so comments and layout stay.

#![allow(deprecated)] // the crate's own uses of its deprecated items

use std::fmt;
use std::ops::Range;
use std::path::Path;

#[derive(Clone, Debug, PartialEq)]
pub enum Value {
    String(String),
    Integer(i64),
    Float(f64),
    Bool(bool),
    Array(Vec<Value>),
    Table(Table),
}

/// Keys and their values, in the order they were written.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Table(Vec<(String, Value)>);

impl Table {
    pub fn get(&self, key: &str) -> Option<&Value> {
        self.0
            .iter()
            .find(|(name, _)| name == key)
            .map(|(_, value)| value)
    }

    /// Adds `key` after the keys already there; the caller has checked that
    /// it is not one of them.
    pub fn push(&mut self, key: String, value: Value) {
        self.0.push((key, value));
    }
}

/// The value as written in a file, which reads back as the same value.
impl fmt::Display for Value {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Value::String(text) => {
                f.write_str("\"")?;
                for c in text.chars() {
                    match c {
                        '"' => f.write_str("\\\"")?,
                        '\\' => f.write_str("\\\\")?,
                        '\n' => f.write_str("\\n")?,
                        '\r' => f.write_str("\\r")?,
                        '\t' => f.write_str("\\t")?,
                        c if c.is_control() => write!(f, "\\u{:04x}", u32::from(c))?,
                        c => write!(f, "{c}")?,
                    }
                }
                f.write_str("\"")
            }
            Value::Integer(integer) => write!(f, "{integer}"),
            Value::Float(float) => write!(f, "{float:?}"), // keeps the `.0` of 1.0
            Value::Bool(bool) => write!(f, "{bool}"),
            Value::Array(values) => {
                f.write_str("[")?;
                for (index, value) in values.iter().enumerate() {
                    if index > 0 {
                        f.write_str(", ")?;
                    }
                    write!(f, "{value}")?;
                }
                f.write_str("]")
            }
            Value::Table(table) if table.0.is_empty() => f.write_str("{}"),
            Value::Table(table) => {
                f.write_str("{ ")?;
                for (index, (key, value)) in table.0.iter().enumerate() {
                    if index > 0 {
                        f.write_str(", ")?;
                    }
                    write!(f, "{key} = {value}")?;
                }
                f.write_str(" }")
            }
        }
    }
}

impl Value {
    /// The text of a string value; None for any other value.
    pub fn as_str(&self) -> Option<&str> {
        match self {
            Value::String(text) => Some(text),
            _ => None,
        }
    }
}

/// The sections of a file, each with its keys, in the order they were
/// written.
#[derive(Debug, Default)]
pub struct Data {
    sections: Vec<(String, Table)>,
}

impl Data {
    /// Whether a section exists. Retained at maintainer request as part of the
    /// TOML API; do not remove it as dead code without asking.
    pub fn has_section(&self, name: &str) -> bool {
        self.section(name).is_some()
    }

    pub fn section(&self, name: &str) -> Option<&Table> {
        let section = self.sections.iter().find(|(section, _)| section == name);
        section.map(|(_, table)| table)
    }

    /// Returns a section, creating it when missing. Retained at maintainer
    /// request as part of the TOML API; do not remove it as dead code without
    /// asking.
    pub fn ensure_section(&mut self, name: &str) -> &mut Table {
        let index = match self
            .sections
            .iter()
            .position(|(section, _)| section == name)
        {
            Some(index) => index,
            None => {
                self.sections.push((name.to_string(), Table::default()));
                self.sections.len() - 1
            }
        };
        &mut self.sections[index].1
    }

    pub fn get(&self, section: &str, key: &str) -> Option<&Value> {
        self.section(section)?.get(key)
    }

    /// Adds a section the file declares; declaring one twice is an error.
    fn declare_section(&mut self, name: &str) -> Result<(), String> {
        if self.has_section(name) {
            return Err(format!("duplicate section {name:?}"));
        }
        self.ensure_section(name);
        Ok(())
    }
}

/// A mistake in a file, at line `line` (counted from 1).
#[derive(Debug, PartialEq)]
pub struct ParseError {
    pub line: usize,
    pub message: String,
}

impl fmt::Display for ParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "line {}: {}", self.line, self.message)
    }
}

impl std::error::Error for ParseError {}

/// Why a file could not be read: it could not be opened, or it is not valid.
#[derive(Debug)]
pub enum FileError {
    Io(std::io::Error),
    Parse(ParseError),
}

impl fmt::Display for FileError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            FileError::Io(error) => error.fmt(f),
            FileError::Parse(error) => error.fmt(f),
        }
    }
}

impl std::error::Error for FileError {}

/// A file's text and what it says. The text is the one source: each change
/// edits it and reads it again.
#[derive(Debug)]
#[deprecated(note = "use toml_edit")]
pub struct Document {
    text: String,
    data: Data,
}

impl Document {
    pub fn parse(text: String) -> Result<Document, ParseError> {
        let data = parse(&text)?;
        Ok(Document { text, data })
    }

    pub fn data(&self) -> &Data {
        &self.data
    }

    pub fn text(&self) -> &str {
        &self.text
    }

    /// Sets `key` of `section` to `value`. An existing value's text is
    /// replaced in place, keeping the line's comment; a new key goes after
    /// the section's last line, and a new section at the end of the file.
    /// Fails, changing nothing, when the edited text would not read back as
    /// `value`, as for a key holding `=`.
    pub fn set(&mut self, section: &str, key: &str, value: &Value) -> Result<(), String> {
        let place = self.find(section, key);
        let mut text = self.text.clone();
        let newline = if text.contains("\r\n") { "\r\n" } else { "\n" };
        match (place.pair, place.end) {
            (Some((_, old)), _) => text.replace_range(old, &value.to_string()),
            (None, Some(end)) => {
                let line = format!("{key} = {value}{newline}");
                if end == 0 || text[..end].ends_with('\n') {
                    text.insert_str(end, &line);
                } else {
                    text.insert_str(end, &format!("{newline}{line}")); // the last line had no break
                }
            }
            (None, None) => {
                if !text.is_empty() {
                    if !text.ends_with('\n') {
                        text.push_str(newline);
                    }
                    text.push_str(newline);
                }
                text.push_str(&format!("[{section}]{newline}{key} = {value}{newline}"));
            }
        }
        let cannot = || format!("cannot write {key} = {value} in section {section:?}");
        let data = parse(&text).map_err(|_| cannot())?;
        if data.get(section, key) != Some(value) {
            return Err(cannot());
        }
        self.text = text;
        self.data = data;
        Ok(())
    }

    /// Removes the line of `key` in `section`, comment and all; false when
    /// there is none. The section's header stays.
    pub fn remove(&mut self, section: &str, key: &str) -> bool {
        let Some((line, _)) = self.find(section, key).pair else {
            return false;
        };
        self.text.replace_range(line, "");
        self.data =
            parse(&self.text).expect("each line reads on its own, so the others stay valid");
        true
    }

    /// Where `key` of `section` is in the text.
    fn find(&self, section: &str, key: &str) -> Place {
        let mut place = Place {
            pair: None,
            end: (section == "default").then_some(0),
        };
        let mut current = "default";
        for (_, bytes, line) in lines(&self.text) {
            match line {
                Ok(Line::Header(name)) => {
                    current = name;
                    if name == section {
                        place.end = Some(bytes.end);
                    }
                }
                Ok(Line::Pair { key: name, value }) if current == section => {
                    if name == key {
                        place.pair = Some((bytes.clone(), value));
                    }
                    place.end = Some(bytes.end);
                }
                _ => {}
            }
        }
        place
    }

    /// Writes the text to `path` through a file beside it, so a failed write
    /// leaves the old file whole.
    pub fn write_file(&self, path: &Path) -> std::io::Result<()> {
        let temporary = path.with_added_extension("tmp");
        std::fs::write(&temporary, &self.text)?;
        std::fs::rename(&temporary, path)
    }
}

/// Where a key is in a document's text.
struct Place {
    pair: Option<(Range<usize>, Range<usize>)>, // the key's line and its value
    end: Option<usize>, // the end of the section's last line, where a new key goes
}

/// Reads the file at `path`, which must be UTF-8, so that writing it back
/// changes no byte it does not set.
#[deprecated(note = "use toml_edit")]
pub fn parse_file(path: &Path) -> Result<Document, FileError> {
    let bytes = std::fs::read(path).map_err(FileError::Io)?;
    let text = String::from_utf8(bytes).map_err(|error| {
        let valid = &error.as_bytes()[..error.utf8_error().valid_up_to()];
        FileError::Parse(ParseError {
            line: valid.iter().filter(|&&byte| byte == b'\n').count() + 1,
            message: "file is not valid UTF-8".to_string(),
        })
    })?;
    Document::parse(text).map_err(FileError::Parse)
}

/// Parses TOML held in memory. Bytes that are not UTF-8 read as U+FFFD.
/// Retained at maintainer request as part of the TOML API; do not remove it as
/// dead code without asking.
#[deprecated(note = "use toml_edit")]
pub fn parse_bytes(src: &[u8]) -> Result<Data, ParseError> {
    parse(&String::from_utf8_lossy(src))
}

#[deprecated(note = "use toml_edit")]
pub fn parse(src: &str) -> Result<Data, ParseError> {
    let mut data = Data::default();
    let mut current_section = "default";
    for (number, _, line) in lines(src) {
        let fail = |message: String| ParseError {
            line: number,
            message,
        };
        match line.map_err(fail)? {
            Line::Blank => {}
            Line::Header(name) => {
                data.declare_section(name).map_err(fail)?;
                current_section = name;
            }
            Line::Pair { key, value } => {
                if data.get(current_section, key).is_some() {
                    return Err(fail(format!(
                        "duplicate key {key:?} in section {current_section:?}"
                    )));
                }
                let parts: Vec<&str> = src[value] // a value over several lines may hold comments
                    .lines()
                    .map(|part| &part[..value_part(part).0])
                    .collect();
                let value = parse_value(&parts.join("\n")).map_err(fail)?;
                data.ensure_section(current_section)
                    .push(key.to_string(), value);
            }
        }
    }
    Ok(data)
}

/// What one item of a file holds.
enum Line<'a> {
    Blank, // empty or a comment
    Header(&'a str),
    Pair { key: &'a str, value: Range<usize> }, // value: where its text is in the file
}

/// Each item of `src`: the number of its first line, where it is in `src`
/// (its line break included) and what it holds. An item is one line, or more
/// when a value in brackets or braces goes on over later lines.
fn lines(src: &str) -> impl Iterator<Item = (usize, Range<usize>, Result<Line<'_>, String>)> {
    let (mut start, mut number) = (0, 1);
    std::iter::from_fn(move || {
        if start == src.len() {
            return None;
        }
        let (end, line) = line(src, start);
        let item = (number, start..end, line);
        number += src[start..end].matches('\n').count();
        start = end;
        Some(item)
    })
}

/// The end of the line that starts at `start`, its line break included.
fn line_end(src: &str, start: usize) -> usize {
    src[start..]
        .find('\n')
        .map_or(src.len(), |index| start + index + 1)
}

/// The item that starts at `start`: where it ends and what it holds.
fn line(src: &str, start: usize) -> (usize, Result<Line<'_>, String>) {
    let mut end = line_end(src, start);
    let text = &src[start..end];
    let trimmed = text.trim();
    if trimmed.is_empty() || trimmed.starts_with('#') {
        return (end, Ok(Line::Blank));
    }
    if let Some(header) = trimmed.strip_prefix('[').and_then(|l| l.strip_suffix(']')) {
        let name = header.trim();
        if name.is_empty() {
            return (end, Err("empty section name".to_string()));
        }
        return (end, Ok(Line::Header(name)));
    }
    let Some(equals) = text.find('=') else {
        return (end, Err(format!("invalid key/value pair: {trimmed}")));
    };
    let key = text[..equals].trim();
    if key.is_empty() {
        return (end, Err("empty key".to_string()));
    }
    let after = &text[equals + 1..];
    let (comment, mut depth) = value_part(after);
    let value = &after[..comment];
    let value_start = start + equals + 1 + (value.len() - value.trim_start().len());
    let mut value_end = value_start + value.trim().len();
    while depth > 0 {
        if end == src.len() {
            return (end, Err("unterminated composite value".to_string()));
        }
        let next = line_end(src, end);
        let (comment, opened) = value_part(&src[end..next]);
        depth += opened;
        let part = src[end..end + comment].trim_end();
        if !part.trim_start().is_empty() {
            value_end = end + part.len();
        }
        end = next;
    }
    let value = value_start..value_end;
    (end, Ok(Line::Pair { key, value }))
}

/// Reads one line's part of a value: where a `#` comment starts outside
/// quotes (the length of `text` when there is none), and how many more
/// brackets and braces open than close before it.
fn value_part(text: &str) -> (usize, isize) {
    let (mut in_quotes, mut escaped, mut depth) = (false, false, 0);
    for (index, c) in text.char_indices() {
        if escaped {
            escaped = false;
        } else if c == '\\' {
            escaped = true;
        } else if c == '"' {
            in_quotes = !in_quotes;
        } else if !in_quotes {
            match c {
                '#' => return (index, depth),
                '[' | '{' => depth += 1,
                ']' | '}' => depth -= 1,
                _ => {}
            }
        }
    }
    (text.len(), depth)
}

fn parse_value(text: &str) -> Result<Value, String> {
    let text = text.trim();
    let enclosed = |open: char, close: char| {
        text.len() >= 2 && text.starts_with(open) && text.ends_with(close)
    };
    if text.is_empty() {
        return Err("missing value".to_string());
    }
    if enclosed('"', '"') {
        return unquote(text).map(Value::String);
    }
    if enclosed('{', '}') {
        return inline_table(&text[1..text.len() - 1]).map(Value::Table);
    }
    if enclosed('[', ']') {
        return array(&text[1..text.len() - 1]).map(Value::Array);
    }
    if text == "true" || text == "false" {
        return Ok(Value::Bool(text == "true"));
    }
    if let Ok(integer) = text.parse() {
        return Ok(Value::Integer(integer));
    }
    if let Ok(float) = text.parse() {
        return Ok(Value::Float(float));
    }
    Err(format!(
        "invalid value {text}; write text in double quotes, like \"{text}\""
    ))
}

fn inline_table(body: &str) -> Result<Table, String> {
    let mut table = Table::default();
    for part in split_top_level(body.trim(), ',')? {
        let Some((key, value_text)) = part.split_once('=') else {
            return Err(format!("invalid inline table entry {part:?}"));
        };
        let key = key.trim();
        if key.is_empty() {
            return Err("empty inline table key".to_string());
        }
        if table.get(key).is_some() {
            return Err(format!("duplicate inline table key {key:?}"));
        }
        table.push(key.to_string(), parse_value(value_text)?);
    }
    Ok(table)
}

fn array(body: &str) -> Result<Vec<Value>, String> {
    split_top_level(body.trim(), ',')?
        .iter()
        .map(|part| parse_value(part))
        .collect()
}

/// Splits `text` at each `delimiter` outside quotes, braces and brackets; the
/// parts are trimmed and a last empty part is dropped.
fn split_top_level(text: &str, delimiter: char) -> Result<Vec<String>, String> {
    let mut parts = Vec::new();
    let mut current = String::new();
    let mut in_quotes = false;
    let mut escaped = false;
    let mut brace_depth = 0usize;
    let mut bracket_depth = 0usize;
    for c in text.chars() {
        if escaped {
            escaped = false;
        } else if c == '\\' {
            escaped = true;
        } else if c == '"' {
            in_quotes = !in_quotes;
        } else if !in_quotes {
            match c {
                '{' => brace_depth += 1,
                '[' => bracket_depth += 1,
                '}' => {
                    brace_depth = brace_depth
                        .checked_sub(1)
                        .ok_or_else(|| "unbalanced '}'".to_string())?;
                }
                ']' => {
                    bracket_depth = bracket_depth
                        .checked_sub(1)
                        .ok_or_else(|| "unbalanced ']'".to_string())?;
                }
                _ if c == delimiter && brace_depth == 0 && bracket_depth == 0 => {
                    parts.push(current.trim().to_string());
                    current.clear();
                    continue;
                }
                _ => {}
            }
        }
        current.push(c);
    }
    if in_quotes || brace_depth != 0 || bracket_depth != 0 {
        return Err("unterminated composite value".to_string());
    }
    if !current.trim().is_empty() {
        parts.push(current.trim().to_string());
    }
    Ok(parts)
}

/// The string a double-quoted literal stands for, with the escapes of Go's
/// strconv.Unquote: `\a \b \f \n \r \t \v \\ \"`, `\xHH`, `\OOO`, `\uHHHH` and
/// `\UHHHHHHHH`.
fn unquote(text: &str) -> Result<String, String> {
    let invalid = || format!("invalid quoted string {text:?}");
    let body = &text[1..text.len() - 1];
    let mut out = String::with_capacity(body.len());
    let mut bytes = Vec::new(); // `\x` and octal escapes write bytes, which must form UTF-8
    let mut chars = body.chars();
    while let Some(c) = chars.next() {
        let c = match c {
            '"' | '\n' => return Err(invalid()),
            '\\' => match chars.next().ok_or_else(invalid)? {
                'a' => '\x07',
                'b' => '\x08',
                'f' => '\x0c',
                'n' => '\n',
                'r' => '\r',
                't' => '\t',
                'v' => '\x0b',
                '\\' => '\\',
                '"' => '"',
                'x' => {
                    bytes.push(digits(&mut chars, 2, 16).ok_or_else(invalid)? as u8);
                    continue;
                }
                'u' => char::from_u32(digits(&mut chars, 4, 16).ok_or_else(invalid)?)
                    .ok_or_else(invalid)?,
                'U' => char::from_u32(digits(&mut chars, 8, 16).ok_or_else(invalid)?)
                    .ok_or_else(invalid)?,
                first @ '0'..='7' => {
                    let rest = digits(&mut chars, 2, 8).ok_or_else(invalid)?;
                    let byte = first.to_digit(8).unwrap_or(0) * 64 + rest;
                    bytes.push(u8::try_from(byte).map_err(|_| invalid())?);
                    continue;
                }
                _ => return Err(invalid()),
            },
            c => c,
        };
        if !bytes.is_empty() {
            out.push_str(std::str::from_utf8(&bytes).map_err(|_| invalid())?);
            bytes.clear();
        }
        out.push(c);
    }
    out.push_str(std::str::from_utf8(&bytes).map_err(|_| invalid())?);
    Ok(out)
}

/// Reads exactly `count` digits in `radix` from `chars`.
fn digits(chars: &mut std::str::Chars<'_>, count: usize, radix: u32) -> Option<u32> {
    let text: String = chars.by_ref().take(count).collect();
    if text.chars().count() != count || !text.chars().all(|c| c.is_digit(radix)) {
        return None;
    }
    u32::from_str_radix(&text, radix).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_sections_inline_tables_and_arrays() {
        let src = r#"
title = "peeper" # the name

[package]
name = "app"
count = 3
ratio = 0.5

[dependencies]
json = { type = "remote", repo = "github.com/acme/json", version = "v1.2.0" }
flags = [true, 1, "x", [2]]
"#;
        let data = parse(src).unwrap();
        assert_eq!(
            data.get("default", "title").and_then(Value::as_str),
            Some("peeper")
        );
        assert_eq!(data.get("package", "count"), Some(&Value::Integer(3)));
        assert_eq!(data.get("package", "ratio"), Some(&Value::Float(0.5)));
        let Some(Value::Table(json)) = data.get("dependencies", "json") else {
            panic!("not a table")
        };
        assert_eq!(
            json.get("repo").and_then(Value::as_str),
            Some("github.com/acme/json")
        );
        let flags = vec![
            Value::Bool(true),
            Value::Integer(1),
            Value::String("x".to_string()),
            Value::Array(vec![Value::Integer(2)]),
        ];
        assert_eq!(
            data.get("dependencies", "flags"),
            Some(&Value::Array(flags))
        );
        let sections: Vec<&str> = data
            .sections
            .iter()
            .map(|(name, _)| name.as_str())
            .collect();
        assert_eq!(sections, ["default", "package", "dependencies"]);
        let package = data.section("package").unwrap();
        let keys: Vec<&str> = package.0.iter().map(|(key, _)| key.as_str()).collect();
        assert_eq!(keys, ["name", "count", "ratio"]);
        assert!(data.has_section("package") && data.section("dev").is_none());
    }

    #[test]
    fn unquotes_like_go() {
        let value =
            |text: &str| parse(&format!("k = {text}")).map(|d| d.get("default", "k").cloned());
        let string = |s: &str| Ok(Some(Value::String(s.to_string())));
        assert_eq!(value(r#""a\tb\"c\\""#), string("a\tb\"c\\"));
        assert_eq!(value(r#""\x41\101é\U0001F600""#), string("AAé😀"));
        assert_eq!(
            value(r##""a # not a comment""##),
            string("a # not a comment")
        );
        for bad in [
            r#""\q""#,
            r#""\x4""#,
            r#""\x+1""#,
            r#""a"b""#,
            "plain words",
        ] {
            assert!(value(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn errors_name_their_line() {
        let cases = [
            (
                "a = 1\na = 2",
                "line 2: duplicate key \"a\" in section \"default\"",
            ),
            ("[x]\n[x]", "line 2: duplicate section \"x\""),
            ("[ ]", "line 1: empty section name"),
            ("no equals", "line 1: invalid key/value pair: no equals"),
            (" = 1", "line 1: empty key"),
            ("a = ", "line 1: missing value"),
            ("a = { b }", "line 1: invalid inline table entry \"b\""),
            ("a = [1, ]]", "line 1: unbalanced ']'"),
            ("a = [\"x]", "line 1: unterminated composite value"),
            (
                "v = 1.2.0",
                "line 1: invalid value 1.2.0; write text in double quotes, like \"1.2.0\"",
            ),
        ];
        for (src, want) in cases {
            assert_eq!(parse(src).unwrap_err().to_string(), want, "{src}");
        }
    }

    #[test]
    fn ensure_section_keeps_what_is_there() {
        let mut data = parse("[a]\nk = 1").unwrap();
        data.ensure_section("a");
        data.ensure_section("b")
            .push("k".to_string(), Value::Bool(true));
        assert_eq!(data.get("a", "k"), Some(&Value::Integer(1)));
        let sections: Vec<&str> = data
            .sections
            .iter()
            .map(|(name, _)| name.as_str())
            .collect();
        assert_eq!(sections, ["a", "b"]);
        assert!(parse_bytes(b"k = \"\xff\"").is_ok());
    }

    #[test]
    fn set_rewrites_only_the_value() {
        let text = "# app\nname = \"app\" # the name\n\n[dependencies]\njson = { repo = \"a\", version = \"v1\" } # pinned\n\n[test]\nmode = \"check\"\n";
        let mut document = Document::parse(text.to_string()).unwrap();
        let mut json = Table::default();
        json.push("repo".to_string(), Value::String("a".to_string()));
        json.push("version".to_string(), Value::String("v2".to_string()));
        document
            .set("dependencies", "json", &Value::Table(json.clone()))
            .unwrap();
        assert_eq!(
            document.text(),
            text.replace("version = \"v1\"", "version = \"v2\"")
        );
        assert_eq!(
            document.data().get("dependencies", "json"),
            Some(&Value::Table(json))
        );
    }

    #[test]
    fn set_adds_keys_and_sections() {
        let mut document = Document::parse("[a]\nk = 1".to_string()).unwrap();
        document.set("a", "j", &Value::Bool(true)).unwrap();
        document
            .set("default", "name", &Value::String("x".to_string()))
            .unwrap();
        document
            .set("b", "list", &Value::Array(Vec::new()))
            .unwrap();
        assert_eq!(
            document.text(),
            "name = \"x\"\n[a]\nk = 1\nj = true\n\n[b]\nlist = []\n"
        );
        let mut empty = Document::parse(String::new()).unwrap();
        empty.set("c", "k", &Value::Integer(2)).unwrap();
        assert_eq!(empty.text(), "[c]\nk = 2\n");
    }

    #[test]
    fn values_go_on_over_lines() {
        let text = "deps = [\n  \"a\", # first\n  \"b\",\n] # done\njson = {\n  repo = \"x\",\n  tags = [1, 2] }\nbad = 1 2\n";
        let error = parse(text).unwrap_err();
        assert_eq!(error.line, 8, "lines after a long value keep their numbers");
        let mut document = Document::parse(text.replace("bad = 1 2\n", "")).unwrap();
        let strings = |names: &[&str]| {
            Value::Array(
                names
                    .iter()
                    .map(|name| Value::String(name.to_string()))
                    .collect(),
            )
        };
        assert_eq!(
            document.data().get("default", "deps"),
            Some(&strings(&["a", "b"]))
        );
        let Some(Value::Table(json)) = document.data().get("default", "json") else {
            panic!("not a table")
        };
        assert_eq!(json.get("repo"), Some(&Value::String("x".to_string())));
        document.set("default", "deps", &strings(&["c"])).unwrap();
        assert!(document.remove("default", "json"));
        assert_eq!(document.text(), "deps = [\"c\"] # done\n");
        assert_eq!(
            parse("a = [\n1,\n").unwrap_err().to_string(),
            "line 1: unterminated composite value"
        );
    }

    #[test]
    fn remove_drops_the_whole_line() {
        let text = "[dependencies]\na = \"1\" # first\nb = \"2\"\n";
        let mut document = Document::parse(text.to_string()).unwrap();
        assert!(document.remove("dependencies", "a"));
        assert!(!document.remove("dependencies", "a"));
        assert!(!document.remove("default", "b"));
        assert_eq!(document.text(), "[dependencies]\nb = \"2\"\n");
        assert_eq!(document.data().get("dependencies", "a"), None);
    }

    #[test]
    fn set_keeps_the_line_breaks_of_the_file() {
        let mut document = Document::parse("[a]\r\nk = 1\r\n".to_string()).unwrap();
        document.set("a", "j", &Value::Integer(2)).unwrap();
        document.set("b", "k", &Value::Integer(3)).unwrap();
        assert_eq!(
            document.text(),
            "[a]\r\nk = 1\r\nj = 2\r\n\r\n[b]\r\nk = 3\r\n"
        );
    }

    #[test]
    fn set_fails_without_change_when_it_cannot_read_back() {
        let mut document = Document::parse("k = 1\n".to_string()).unwrap();
        for (section, key) in [("default", "a = b"), ("default", "a\nb"), ("x\ny", "k")] {
            assert!(
                document.set(section, key, &Value::Integer(2)).is_err(),
                "{key}"
            );
        }
        assert!(
            document
                .set("default", "k", &Value::Float(f64::NAN))
                .is_err()
        );
        assert_eq!(document.text(), "k = 1\n");
    }

    #[test]
    fn values_print_as_they_read() {
        let mut table = Table::default();
        table.push(
            "t".to_string(),
            Value::String("a\"b\\c\nd\u{1}é".to_string()),
        );
        table.push("f".to_string(), Value::Float(1.0));
        let values = [
            Value::Table(table),
            Value::Table(Table::default()),
            Value::Array(vec![Value::Integer(-3), Value::Array(Vec::new())]),
        ];
        for value in values {
            let data = parse(&format!("k = {value}")).unwrap();
            assert_eq!(data.get("default", "k"), Some(&value), "{value}");
        }
    }

    #[test]
    fn write_file_replaces_the_file() {
        let folder = std::env::temp_dir().join(format!("peeper-toml-{}", std::process::id()));
        std::fs::create_dir_all(&folder).unwrap();
        let path = folder.join("peeper.toml");
        std::fs::write(&path, "k = 1 # kept\n").unwrap();
        let mut document = parse_file(&path).unwrap();
        document.set("default", "k", &Value::Integer(2)).unwrap();
        document.write_file(&path).unwrap();
        assert_eq!(std::fs::read_to_string(&path).unwrap(), "k = 2 # kept\n");
        assert!(!path.with_added_extension("tmp").exists());
        std::fs::write(&path, b"k = 1\n# \xff\n").unwrap();
        assert_eq!(
            parse_file(&path).unwrap_err().to_string(),
            "line 2: file is not valid UTF-8"
        );
        _ = std::fs::remove_dir_all(&folder);
    }
}
