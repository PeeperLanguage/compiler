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
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := Lex("test.em", src, diag)
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
