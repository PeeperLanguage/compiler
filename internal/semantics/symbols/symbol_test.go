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

func TestNewPublishesLetMutability(t *testing.T) {
	location := ast.LocOf(&ast.Ident{})
	declaration := &ast.LetDecl{IsMutable: true, MutableLocation: location}
	sym := New("value", SymbolVar, declaration, nil)
	if !sym.IsMutable() || sym.MutableLocation != location {
		t.Fatalf("let mutability = (%v, %#v), want (true, %#v)", sym.IsMutable(), sym.MutableLocation, location)
	}
}

func TestFunctionScopeHasConcreteType(t *testing.T) {
	sym := New("main", SymbolFunc, nil, nil)
	var scope *Scope = sym.Scope
	if scope != nil {
		t.Fatalf("new function scope = %v, want nil", scope)
	}
}
