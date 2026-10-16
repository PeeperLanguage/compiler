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

func TestEscapeErrorsPointAtTheEscape(t *testing.T) {
	text := `"a\q b\xZZ c" 'é' '\xC3\xA9' 'ab' b'\x41'`
	tokens, errs := Scan(source.NewFile("t.peep", text))
	var got []string
	for _, err := range errs {
		span := err.Labels[0].Span
		got = append(got, text[span.Start:span.End]+": "+err.Message)
	}
	want := []string{
		`\q: unknown escape sequence "\\q"`,
		`\xZZ: invalid hex escape "\\xZZ"`,
		`'ab': character literal must contain exactly one character`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("errors\n got: %q\nwant: %q", got, want)
	}
	if len(tokens) != 6 { // five literals and EOF; invalid literals keep their tokens
		t.Fatalf("%d tokens", len(tokens))
	}
}

// TestNumberDigits holds the old compiler's messages for digits outside a
// number's base. The suffix and a leading zero are the parser's to check, so
// those literals give no lexical error. A literal keeps its single token
// whether or not it is valid.
func TestNumberDigits(t *testing.T) {
	for _, test := range []struct{ text, message string }{
		{"0xFG", "invalid hexadecimal literal 0xFG"},
		{"0o8", "invalid octal literal 0o8"},
		{"0b102u8", "invalid binary literal 0b102"},
		{"0xu8", "invalid hexadecimal literal 0x"},
		{"0", ""}, {"0x1f_u8", ""}, {"0b1_0", ""}, {"0XAB", ""}, {"0xffu24", ""}, {"1.5f32", ""},
		{"1i", ""}, {"2.4i32", ""}, {"007", ""}, {"0b1i", ""},
	} {
		tokens, errs := Scan(source.NewFile("t.peep", test.text))
		if len(tokens) != 2 || tokens[0].End != len(test.text) {
			t.Errorf("%s: not one number token: %v", test.text, tokens)
			continue
		}
		var got string
		if len(errs) > 0 {
			got = errs[0].Message
			if len(errs) > 1 || errs[0].Labels[0].Span != tokens[0].Span {
				t.Errorf("%s: errors %v, want one on the whole literal", test.text, errs)
			}
		}
		if got != test.message {
			t.Errorf("%s: got %q, want %q", test.text, got, test.message)
		}
	}
}
