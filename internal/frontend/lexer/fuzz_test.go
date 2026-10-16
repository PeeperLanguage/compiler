package lexer

import (
	"regexp"
	"testing"

	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

// The token rules as regular expressions: the specification the hand-written
// scanner is checked against. They are the old regex lexer's rules, in its
// order; earlier rules win.
const (
	refHex     = `0[xX][0-9A-Za-z](?:[0-9A-Za-z]|_[0-9A-Za-z])*`
	refOct     = `0[oO][0-9A-Za-z](?:[0-9A-Za-z]|_[0-9A-Za-z])*`
	refBin     = `0[bB][0-9A-Za-z](?:[0-9A-Za-z]|_[0-9A-Za-z])*`
	refDec     = `[0-9](?:[0-9]|_[0-9])*`
	refFloat   = refDec + `(?:\.[0-9](?:[0-9]|_[0-9])*)?(?:[eE][+-]?[0-9](?:[0-9]|_[0-9])*)?`
	refNumber  = `(?:` + refHex + `|` + refOct + `|` + refBin + `|` + refFloat + `)(?:[iuf](?:[0-9_]+)?)?`
	refSkipped = token.Kind(255) // whitespace and comments other than doc comments
)

var reference = func() []struct {
	re   *regexp.Regexp
	kind token.Kind
} {
	rules := []struct {
		pattern string
		kind    token.Kind
	}{
		{`\s+`, refSkipped},
		{`///[^\n\r]*`, token.DOC_COMMENT},
		{`//[^\n\r]*`, refSkipped},
		{`(?s)/\*.*?\*/`, refSkipped},
		{`c"(?:\\.|[^"\\])*"`, token.CSTRING},
		{`"(?:\\.|[^"\\])*"`, token.STRING},
		{`b'(?:\\.|[^'\\])*'`, token.BYTE_CHAR},
		{`'(?:\\.|[^'\\])*'`, token.CHAR},
		{refNumber, token.NUMBER},
		{`[A-Za-z_][A-Za-z0-9_]*`, token.IDENT}, // keywords are told apart below
	}
	for _, op := range token.Operators {
		rules = append(rules, struct {
			pattern string
			kind    token.Kind
		}{regexp.QuoteMeta(op.Text), op.Kind})
	}
	out := make([]struct {
		re   *regexp.Regexp
		kind token.Kind
	}, len(rules))
	for i, rule := range rules {
		out[i].re = regexp.MustCompile("^(?:" + rule.pattern + ")")
		out[i].kind = rule.kind
	}
	return out
}()

// referenceScan lexes text with the regular expressions. It reports false when
// some text matches no rule.
func referenceScan(text string) ([]token.Token, bool) {
	var tokens []token.Token
	for pos := 0; pos < len(text); {
		matched := false
		for _, rule := range reference {
			loc := rule.re.FindStringIndex(text[pos:])
			if loc == nil {
				continue
			}
			kind := rule.kind
			if kind == token.IDENT {
				kind = token.LookupIdent(text[pos : pos+loc[1]])
			}
			if kind != refSkipped {
				tokens = append(tokens, token.Token{Kind: kind, Span: source.Span{Start: pos, End: pos + loc[1]}})
			}
			pos += loc[1]
			matched = true
			break
		}
		if !matched {
			return nil, false
		}
	}
	return tokens, true
}

// FuzzScan checks Scan on any input: tokens stay in order inside the text and
// end with EOF; and when Scan finds no error, its tokens are exactly those the
// regular expressions give. Run it with: go test -fuzz=FuzzScan ./internal/frontend/lexer
func FuzzScan(f *testing.F) {
	for _, seed := range []string{
		"fn main() -> i32 { return 0; }",
		`import "core:io" as io; let s = c"a\n" + "b\"c";`,
		"0x1F_ff 0b1 1_000 1.5 1..2 1e5 1e-3 1i32 2.5f64 0o7",
		"/// doc\n// note\n/* block\n */ x <<= y ?? z |> w ..= ... ::",
		`'a' '\'' b'\x41' '\t' "multi
line"`,
		"a$b", `"open`, "/* open", "café", `'ab'`, `"\q"`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		tokens, errs := Scan(source.NewFile("fuzz.peep", text))
		last := len(tokens) - 1
		if last < 0 || tokens[last].Kind != token.EOF || tokens[last].Start != len(text) {
			t.Fatalf("%q: tokens do not end with EOF at the end of the text", text)
		}
		for i, tok := range tokens {
			if tok.Start < 0 || tok.End < tok.Start || tok.End > len(text) || i > 0 && tok.Start < tokens[i-1].End {
				t.Fatalf("%q: token %d %v is out of order or outside the text", text, i, tok)
			}
		}
		if len(errs) > 0 {
			return
		}
		want, ok := referenceScan(text)
		if !ok {
			t.Fatalf("%q: Scan found no error, but the regular expressions match no rule", text)
		}
		if len(want) != last {
			t.Fatalf("%q: %d tokens, regular expressions give %d", text, last, len(want))
		}
		for i := range want {
			if tokens[i] != want[i] {
				t.Fatalf("%q: token %d is %v %d-%d, regular expressions give %v %d-%d", text, i,
					tokens[i].Kind, tokens[i].Start, tokens[i].End, want[i].Kind, want[i].Start, want[i].End)
			}
		}
	})
}
