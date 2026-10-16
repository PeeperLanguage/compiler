package literal

import "testing"

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
