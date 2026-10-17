//! Reads the small part of TOML that project files use, one line at a time:
//! `[section]` headers, `key = value` pairs, and `#` comments. A value is a
//! string in double quotes, an integer, a float, `true` or `false`, an array
//! `[a, b]` or an inline table `{ key = value }`, all on one line; any other
//! text is kept as a string. Keys before the first header are in the section
//! "default".

use std::collections::HashMap;
use std::fmt;
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

pub type Table = HashMap<String, Value>;

impl Value {
    /// The text of a string value; None for any other value.
    pub fn as_str(&self) -> Option<&str> {
        match self {
            Value::String(text) => Some(text),
            _ => None,
        }
    }
}

/// The sections of a file, each with its keys, and the order both were
/// written in.
#[derive(Debug, Default)]
pub struct Data {
    pub sections: HashMap<String, Table>,
    pub section_order: Vec<String>,
    pub key_order: HashMap<String, Vec<String>>,
}

impl Data {
    /// Whether a section exists. Retained at maintainer request as part of the
    /// TOML API; do not remove it as dead code without asking.
    pub fn has_section(&self, name: &str) -> bool {
        self.sections.contains_key(name)
    }

    pub fn section(&self, name: &str) -> Option<&Table> {
        self.sections.get(name)
    }

    /// Returns a section, creating it when missing. Retained at maintainer
    /// request as part of the TOML API; do not remove it as dead code without
    /// asking.
    pub fn ensure_section(&mut self, name: &str) -> &mut Table {
        if !self.sections.contains_key(name) {
            self.section_order.push(name.to_string());
        }
        self.sections.entry(name.to_string()).or_default()
    }

    pub fn get(&self, section: &str, key: &str) -> Option<&Value> {
        self.sections.get(section)?.get(key)
    }

    /// Adds a section the file declares; declaring one twice is an error.
    fn declare_section(&mut self, name: &str) -> Result<(), String> {
        if self.sections.contains_key(name) {
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

pub fn parse_file(path: &Path) -> Result<Data, FileError> {
    let bytes = std::fs::read(path).map_err(FileError::Io)?;
    parse_bytes(&bytes).map_err(FileError::Parse)
}

/// Parses TOML held in memory. Bytes that are not UTF-8 read as U+FFFD.
/// Retained at maintainer request as part of the TOML API; do not remove it as
/// dead code without asking.
pub fn parse_bytes(src: &[u8]) -> Result<Data, ParseError> {
    parse(&String::from_utf8_lossy(src))
}

pub fn parse(src: &str) -> Result<Data, ParseError> {
    let mut data = Data::default();
    let mut current_section = "default".to_string();
    for (index, line) in src.lines().enumerate() {
        let line_number = index + 1;
        let fail = |message: String| ParseError {
            line: line_number,
            message,
        };
        let line = line.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        if let Some(header) = line.strip_prefix('[').and_then(|l| l.strip_suffix(']')) {
            let name = header.trim();
            if name.is_empty() {
                return Err(fail("empty section name".to_string()));
            }
            data.declare_section(name).map_err(fail)?;
            current_section = name.to_string();
            continue;
        }
        key_value(&mut data, &current_section, line).map_err(fail)?;
    }
    Ok(data)
}

fn key_value(data: &mut Data, section: &str, line: &str) -> Result<(), String> {
    let Some((key, value_text)) = line.split_once('=') else {
        return Err(format!("invalid key/value pair: {line}"));
    };
    let key = key.trim();
    if key.is_empty() {
        return Err("empty key".to_string());
    }
    let value_text = strip_inline_comment(value_text.trim());
    if data.get(section, key).is_some() {
        return Err(format!("duplicate key {key:?} in section {section:?}"));
    }
    let value = parse_value(&value_text)?;
    data.ensure_section(section).insert(key.to_string(), value);
    data.key_order
        .entry(section.to_string())
        .or_default()
        .push(key.to_string());
    Ok(())
}

/// `value_text` without a `#` comment that starts outside quotes.
fn strip_inline_comment(value_text: &str) -> String {
    let mut out = String::with_capacity(value_text.len());
    let mut in_quotes = false;
    let mut escaped = false;
    for c in value_text.chars() {
        if escaped {
            escaped = false;
        } else if c == '\\' {
            escaped = true;
        } else if c == '"' {
            in_quotes = !in_quotes;
        } else if c == '#' && !in_quotes {
            break;
        }
        out.push(c);
    }
    out.trim().to_string()
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
    Ok(Value::String(text.to_string()))
}

fn inline_table(body: &str) -> Result<Table, String> {
    let mut table = Table::new();
    for part in split_top_level(body.trim(), ',')? {
        let Some((key, value_text)) = part.split_once('=') else {
            return Err(format!("invalid inline table entry {part:?}"));
        };
        let key = key.trim();
        if key.is_empty() {
            return Err("empty inline table key".to_string());
        }
        if table.contains_key(key) {
            return Err(format!("duplicate inline table key {key:?}"));
        }
        table.insert(key.to_string(), parse_value(value_text)?);
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
    if !current.is_empty() {
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
        assert_eq!(json["repo"].as_str(), Some("github.com/acme/json"));
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
        assert_eq!(data.section_order, ["default", "package", "dependencies"]);
        assert_eq!(data.key_order["package"], ["name", "count", "ratio"]);
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
        assert_eq!(value("plain words"), string("plain words"));
        for bad in [r#""\q""#, r#""\x4""#, r#""\x+1""#, r#""a"b""#] {
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
            .insert("k".to_string(), Value::Bool(true));
        assert_eq!(data.get("a", "k"), Some(&Value::Integer(1)));
        assert_eq!(data.section_order, ["a", "b"]);
        assert!(parse_bytes(b"k = \"\xff\"").is_ok());
    }
}
