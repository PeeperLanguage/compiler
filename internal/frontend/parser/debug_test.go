package parser

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/pkg/peeper"
	"testing"
)

func TestDebug(t *testing.T) {
	src := `// fn docs
fn main() -> i32 {
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag()
	stream := lexer.New("test"+peeper.SourceExt, src, diag).Tokenize()
	for _, tok := range stream {
		t.Logf("Token: %v", tok.Kind)
	}
}
