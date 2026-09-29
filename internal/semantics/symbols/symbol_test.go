package symbols

import (
	"testing"

	"compiler/internal/frontend/ast"
)

func TestNewHandlesTypedNilNode(t *testing.T) {
	var importDecl *ast.ImportDecl

	sym := New(ProjectedSymbolID(SymbolImport, "external"), "external", SymbolImport, importDecl, ast.LocOf(importDecl))
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
	sym := New(ProjectedSymbolID(SymbolVar, "value"), "value", SymbolVar, declaration, nil)
	if !sym.IsMutable() || sym.MutableLocation != location {
		t.Fatalf("let mutability = (%v, %#v), want (true, %#v)", sym.IsMutable(), sym.MutableLocation, location)
	}
}

func TestNewPublishesExternalLinkNameWithoutRetainingSyntaxLookup(t *testing.T) {
	for _, test := range []struct {
		name       string
		attributes []ast.Attribute
		body       *ast.BlockStmt
	}{
		{name: "external default", attributes: []ast.Attribute{{Name: ast.AttributeExtern}}},
		{name: "external renamed", attributes: []ast.Attribute{{Name: ast.AttributeExtern, Args: []ast.Expr{&ast.StringLit{Value: "native_main"}}}}},
		{name: "external empty", attributes: []ast.Attribute{{Name: ast.AttributeExtern, Args: []ast.Expr{&ast.StringLit{Value: ""}}}}},
		{name: "body not external", attributes: []ast.Attribute{{Name: ast.AttributeExtern}}, body: &ast.BlockStmt{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			declaration := &ast.FnDecl{Attributed: ast.Attributed{Attributes: test.attributes}, Body: test.body}
			sym := New(ProjectedSymbolID(SymbolFunc, "main"), "main", SymbolFunc, declaration, nil)
			sym.ASTNode = nil
			want, external := ast.FunctionLinkName(declaration, "main")
			if external != (sym.ExternalLinkName != nil) || external && *sym.ExternalLinkName != want {
				t.Fatalf("external link = %#v, want (%q, %t)", sym.ExternalLinkName, want, external)
			}
		})
	}
}

func TestIndexTracksActivityByCanonicalSymbolID(t *testing.T) {
	index := NewIndex()
	identity := ProjectedSymbolID(SymbolVar, "value")
	first := New(identity, "value", SymbolVar, nil, nil)
	second := New(identity, "value", SymbolVar, nil, nil)

	index.MarkUsed(first)
	index.RequireMutable(first)
	if !index.IsUsed(second) || !index.RequiresMutable(second) {
		t.Fatal("symbol activity depends on object identity")
	}
	count := 0
	index.ForEachUsedSymbolID(func(id SymbolID) {
		if id != identity {
			t.Fatalf("used symbol ID = %v, want %v", id, identity)
		}
		count++
	})
	if count != 1 {
		t.Fatalf("used symbol count = %d, want 1", count)
	}
}

func TestFunctionScopeHasConcreteType(t *testing.T) {
	sym := New(ProjectedSymbolID(SymbolFunc, "main"), "main", SymbolFunc, nil, nil)
	var scope *Scope = sym.Scope
	if scope != nil {
		t.Fatalf("new function scope = %v, want nil", scope)
	}
}
