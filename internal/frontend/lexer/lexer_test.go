package lexer

import (
	"testing"

	"compiler/pkg/diagnostics"
	"compiler/internal/tokens"
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
	kinds := []tokens.Kind{
		tokens.IMPORT, tokens.STRING, tokens.AS, tokens.IDENT, tokens.SEMICOLON,
		tokens.CONST, tokens.IDENT, tokens.COLON, tokens.IDENT, tokens.ASSIGN, tokens.NUMBER, tokens.PLUS, tokens.NUMBER, tokens.ASTERISK, tokens.NUMBER, tokens.SEMICOLON,
		tokens.LET, tokens.MUT, tokens.IDENT, tokens.COLON, tokens.IDENT, tokens.ASSIGN, tokens.IDENT, tokens.SEMICOLON,
		tokens.FN, tokens.IDENT, tokens.LPAREN, tokens.IDENT, tokens.COLON, tokens.IDENT, tokens.COMMA, tokens.IDENT, tokens.COLON, tokens.IDENT, tokens.RPAREN, tokens.COLON, tokens.IDENT,
		tokens.LBRACE,
		tokens.LET, tokens.IDENT, tokens.COLON, tokens.IDENT, tokens.ASSIGN, tokens.IDENT, tokens.PLUS, tokens.IDENT, tokens.SEMICOLON,
		tokens.RETURN, tokens.IDENT, tokens.SEMICOLON,
		tokens.RBRACE,
		tokens.EOF,
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
