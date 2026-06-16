package parser

import (
	"testing"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
)

func TestDebug(t *testing.T) {
	src := `// fn docs
fn main() -> i32 {
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.peep")
	stream := lexer.New("test.peep", src, diag).Tokenize()
	for _, tok := range stream {
		t.Logf("Token: %v", tok.Kind)
	}
}
