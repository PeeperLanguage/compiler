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
			sym := New("main", SymbolFunc, declaration, nil)
			sym.ASTNode = nil
			want, external := ast.FunctionLinkName(declaration, "main")
			if external != (sym.ExternalLinkName != nil) || external && *sym.ExternalLinkName != want {
				t.Fatalf("external link = %#v, want (%q, %t)", sym.ExternalLinkName, want, external)
			}
		})
	}
}

func TestFunctionScopeHasConcreteType(t *testing.T) {
	sym := New("main", SymbolFunc, nil, nil)
	var scope *Scope = sym.Scope
	if scope != nil {
		t.Fatalf("new function scope = %v, want nil", scope)
	}
}
