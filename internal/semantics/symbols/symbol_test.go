package symbols

import (
	"testing"

	"compiler/internal/frontend/ast"
)

func TestNewHandlesTypedNilNode(t *testing.T) {
	var importDecl *ast.ImportDecl

	sym := New("external", SymbolImport, importDecl, ast.LocOf(importDecl))
	if sym == nil {
		t.Fatalf("expected symbol")
	}
	if sym.Location != nil {
		t.Fatalf("expected nil location for typed nil node, got %#v", sym.Location)
	}
}
