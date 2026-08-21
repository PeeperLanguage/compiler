package lexer

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/token"
	"compiler/pkg/peeper"
)

func TestLexSubsetProgram(t *testing.T) {
	src := `import "math" as m;
const x: i32 = 1 + 2 * 3;
let mut y: i32 = x;
	fn add(a: i32, b: i32): i32 {
	let z: i32 = a + b;
	return z;
}`
	diag := diagnostics.NewDiagnosticBag()
	stream := New("test"+peeper.SourceExt, src, diag).Tokenize()
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	kinds := []token.Kind{
		token.IMPORT, token.STRING, token.AS, token.IDENT, token.SEMICOLON,
		token.CONST, token.IDENT, token.COLON, token.IDENT, token.ASSIGN, token.NUMBER, token.PLUS, token.NUMBER, token.ASTERISK, token.NUMBER, token.SEMICOLON,
		token.LET, token.MUT, token.IDENT, token.COLON, token.IDENT, token.ASSIGN, token.IDENT, token.SEMICOLON,
		token.FN, token.IDENT, token.LPAREN, token.IDENT, token.COLON, token.IDENT, token.COMMA, token.IDENT, token.COLON, token.IDENT, token.RPAREN, token.COLON, token.IDENT,
		token.LBRACE,
		token.LET, token.IDENT, token.COLON, token.IDENT, token.ASSIGN, token.IDENT, token.PLUS, token.IDENT, token.SEMICOLON,
		token.RETURN, token.IDENT, token.SEMICOLON,
		token.RBRACE,
		token.EOF,
	}
	if len(stream) != len(kinds) {
		t.Fatalf("token length mismatch: got=%d want=%d", len(stream), len(kinds))
	}
	for i, k := range kinds {
		if stream[i].Kind != k {
			t.Fatalf("token[%d]: got %s want %s", i, stream[i].Kind, k)
		}
	}
}

func TestLexEmitsCommentTokens(t *testing.T) {
	src := `/// module docs
// more docs
fn main() -> i32 {
	// not docs
	if true {
		return 0;
	}
	return 1;
}`
	diag := diagnostics.NewDiagnosticBag()
	stream := New("test"+peeper.SourceExt, src, diag).Tokenize()
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	var docs []string
	for _, tok := range stream {
		if tok.Kind == token.DOC_COMMENT {
			docs = append(docs, tok.Literal)
		}
	}
	if len(docs) != 1 || docs[0] != "module docs" {
		t.Fatalf("doc tokens mismatch: %#v", docs)
	}
}

func TestLexBitwiseOperatorsLongestFirst(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	stream := New("test"+peeper.SourceExt, `a & b | c ^ ~d << e >> f && g || h`, diag).Tokenize()
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	want := []token.Kind{
		token.IDENT, token.AMP, token.IDENT, token.BAR, token.IDENT, token.CARET,
		token.TILDE, token.IDENT, token.SHL, token.IDENT, token.SHR, token.IDENT,
		token.ANDAND, token.IDENT, token.OROR, token.IDENT, token.EOF,
	}
	if len(stream) != len(want) {
		t.Fatalf("token length mismatch: got=%d want=%d", len(stream), len(want))
	}
	for i, kind := range want {
		if stream[i].Kind != kind {
			t.Fatalf("token[%d]: got %s want %s", i, stream[i].Kind, kind)
		}
	}
}

func TestLexLiteralKinds(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	stream := New("literals"+peeper.SourceExt, `"str" c"cstr" b'X' 'λ'`, diag).Tokenize()
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	wantKinds := []token.Kind{token.STRING, token.CSTRING, token.BYTE_CHAR, token.CHAR, token.EOF}
	wantValues := []string{"str", "cstr", "X", "λ", ""}
	if len(stream) != len(wantKinds) {
		t.Fatalf("token length mismatch: got=%d want=%d", len(stream), len(wantKinds))
	}
	for i := range wantKinds {
		if stream[i].Kind != wantKinds[i] || stream[i].Literal != wantValues[i] {
			t.Fatalf("token[%d] = (%s, %q), want (%s, %q)", i, stream[i].Kind, stream[i].Literal, wantKinds[i], wantValues[i])
		}
	}
}

func TestLexRejectsMalformedHexEscape(t *testing.T) {
	diag := diagnostics.NewDiagnosticBag()
	stream := New(
		"hex_escape"+peeper.SourceExt,
		`"\x4Z"`,
		diag,
	).Tokenize()

	if !diag.HasErrors() {
		t.Fatalf(
			"expected malformed hex escape to produce a diagnostic, got tokens: %#v",
			stream,
		)
	}
}

func BenchmarkTokenize(b *testing.B) {
	const src = `import "math" as m;
const x: i32 = 1 + 2 * 3;
let mut y: i32 = x;
fn add(a: i32, b: i32): i32 {
	return a + b;
}`

	for b.Loop() {
		New("benchmark"+peeper.SourceExt, src, nil).Tokenize()
	}
}
