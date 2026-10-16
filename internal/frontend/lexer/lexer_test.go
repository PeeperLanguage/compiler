package lexer

import (
	"strings"
	"testing"

	"compiler/internal/source"
)

// scan returns the tokens of text as "kind:text" (EOF left out) and the error
// messages.
func scan(text string) (string, []string) {
	tokens, errs := Scan(source.NewFile("test.peep", text))
	parts := make([]string, 0, len(tokens))
	for _, tok := range tokens[:len(tokens)-1] {
		parts = append(parts, tok.Kind.String()+":"+text[tok.Start:tok.End])
	}
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		messages = append(messages, err.Message)
	}
	return strings.Join(parts, " "), messages
}

func TestScan(t *testing.T) {
	for _, test := range []struct {
		name, text, tokens string
		errors             []string
	}{
		{"declaration", "let mut y: i32 = x;",
			"let:let mut:mut identifier:y ::: identifier:i32 =:= identifier:x ;:;", nil},
		{"comments", "/// doc\n//// doc too\n// note\n/* block */ x",
			"document comment:/// doc document comment://// doc too identifier:x", nil},
		{"longest operators first", "<<= ||> |> ..= ... :: -> !== ??",
			"<<:<< =:= ||:|| >:> |>:|> ..=:..= ...:... ::::: ->:-> !=:!= =:= ??:??", nil},
		{"literals", `"s" c"c" 'x' b'y' 42`,
			`string:"s" c string:c"c" character:'x' byte character:b'y' number:42`, nil},
		{"numbers", "0x1F_ff 0b1 1_000 1.5 1..2 1e5 1e 1i32 0x",
			"number:0x1F_ff number:0b1 number:1_000 number:1.5 number:1 ..:.. number:2 number:1e5 number:1 identifier:e number:1i32 number:0 identifier:x", nil},
		{"unclosed block comment", "x /* y", "identifier:x", []string{"unterminated block comment"}},
		{"string over two lines", "\"a\nb\" x", "string:\"a\nb\" identifier:x", nil},
		{"unclosed string ends with its line", "\"open\nx", `string:"open identifier:x`,
			[]string{"unterminated string literal"}},
		{"unclosed c string", `c"open`, `c string:c"open`, []string{"unterminated string literal"}},
		{"unclosed character", "b'x\ny", "byte character:b'x identifier:y", []string{"unterminated character literal"}},
		{"bad escape keeps its token", `"a\q" x`, `string:"a\q" identifier:x`,
			[]string{`unknown escape sequence "\\q"`}},
		{"character count keeps the tokens", `'ab' b'é'`, `character:'ab' byte character:b'é'`,
			[]string{"character literal must contain exactly one character", "byte literal must contain exactly one byte"}},
		{"non-ASCII name", "café x", "identifier:café identifier:x",
			[]string{"name `café` uses characters that are not allowed in names"}},
		{"illegal character", "a $ b", "identifier:a identifier:b", []string{`illegal character "$"`}},
	} {
		tokens, errors := scan(test.text)
		if tokens != test.tokens {
			t.Errorf("%s: tokens\n got: %s\nwant: %s", test.name, tokens, test.tokens)
		}
		if strings.Join(errors, "\n") != strings.Join(test.errors, "\n") {
			t.Errorf("%s: errors\n got: %q\nwant: %q", test.name, errors, test.errors)
		}
	}
}

func TestUnquote(t *testing.T) {
	for _, test := range []struct {
		text  string
		quote byte
		want  string
		err   string
	}{
		{`plain`, '"', "plain", ""},
		{`a\n\t\\\"\x41`, '"', "a\n\t\\\"A", ""},
		{`\'`, '\'', "'", ""},
		{`\'`, '"', "", `invalid escape sequence \'`},
		{`\xZZ`, '"', "", `invalid hex escape "\\xZZ"`},
		{`\x4`, '"', "", `hex escape \x requires exactly two hexadecimal digits`},
	} {
		got, err := unquote(test.text, test.quote)
		message := ""
		if err != nil {
			message = err.Error()
		}
		if got != test.want || message != test.err {
			t.Errorf("unquote(%q) = %q, %q; want %q, %q", test.text, got, message, test.want, test.err)
		}
	}
}

func TestValue(t *testing.T) {
	for text, want := range map[string]string{
		`"a\tb"`:  "a\tb",
		`c"c"`:    "c",
		`'x'`:     "x",
		`b'\x41'`: "A",
		`"open`:   "open", // unterminated: reported by the lexer
		`"a\q"`:   `a\q`,  // bad escape: reported by the lexer
	} {
		if got := Value(text); got != want {
			t.Errorf("Value(%s) = %q, want %q", text, got, want)
		}
	}
}
