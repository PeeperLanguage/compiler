package table

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
)

func TestScopeDeclareAndLookup(t *testing.T) {
	global := New(nil)
	sx := symbols.New("x", symbols.SymbolVar, nil, ast.LocOf(nil))
	if err := global.Declare(sx); err != nil {
		t.Fatalf("declare x failed: %v", err)
	}
	if err := global.Declare(sx); err == nil {
		t.Fatalf("duplicate declaration should fail")
	}
	if got, ok := global.LookupLocal("x"); !ok || got != sx {
		t.Fatalf("lookup local x failed")
	}

	child := New(global)
	if got, ok := child.Lookup("x"); !ok || got != sx {
		t.Fatalf("child should resolve parent symbol")
	}
	if _, ok := child.LookupLocal("x"); ok {
		t.Fatalf("lookup local should not see parent symbol")
	}
}

func TestScopeSymbolsOrder(t *testing.T) {
	s := New(nil)
	a := symbols.New("a", symbols.SymbolVar, nil, ast.LocOf(nil))
	b := symbols.New("b", symbols.SymbolVar, nil, ast.LocOf(nil))
	if err := s.Declare(a); err != nil {
		t.Fatalf("declare a failed: %v", err)
	}
	if err := s.Declare(b); err != nil {
		t.Fatalf("declare b failed: %v", err)
	}
	got := s.Symbols()
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("unexpected symbol order: %#v", got)
	}
}
