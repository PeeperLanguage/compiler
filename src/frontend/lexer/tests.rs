use super::*;

/// The tokens of `text` as "kind:text" (end of file left out) and the error
/// messages.
fn scan_text(text: &str) -> (String, Vec<String>) {
    let file = Arc::new(SourceFile::new("test.peep", text));
    let (tokens, errors) = scan(&file);
    let parts: Vec<String> = tokens[..tokens.len() - 1]
        .iter()
        .map(|t| format!("{}:{}", t.kind.name(), file.slice(t.span)))
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
