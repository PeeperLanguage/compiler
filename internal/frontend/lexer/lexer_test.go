package lexer

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/token"
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
	stream := New("test.peep", src, diag).Tokenize()
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

func TestLexKeepsStandaloneComments(t *testing.T) {
	src := `// module docs
// more docs
fn main() -> i32 {
	// not docs
	if true {
		return 0;
	}
	return 1;
}`
	diag := diagnostics.NewDiagnosticBag()
	stream := New("test.peep", src, diag).Tokenize()
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	var docs []string
	for _, tok := range stream {
		if tok.Kind == token.DOC_COMMENT {
			docs = append(docs, tok.Literal)
		}
	}
	if len(docs) != 2 {
		t.Fatalf("doc comment count: got %d want 2", len(docs))
	}
	expectedFirst := "module docs\nmore docs"
	if docs[0] != expectedFirst {
		t.Fatalf("first doc group: got %q want %q", docs[0], expectedFirst)
	}
}
