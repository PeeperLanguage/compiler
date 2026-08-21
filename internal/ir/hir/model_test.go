package hir

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/source"
)

func TestSourceInfoOfStatement(t *testing.T) {
	loc := source.NewLocation("main.peep", source.Position{Line: 2, Column: 3}, source.Position{Line: 2, Column: 7})
	stmt := &Binding{NodeID: 41, Location: loc}
	info := SourceInfoOf(stmt)
	if info.NodeID != 41 || info.Location != loc {
		t.Fatalf("source info = %#v, want node 41 and original location", info)
	}
	if got := SourceInfoOf(nil); got != (ir.SourceInfo{}) {
		t.Fatalf("nil source info = %#v, want zero value", got)
	}
}

func TestUninitializedBindingTextOmitsInitializer(t *testing.T) {
	module := &Module{Name: "test", Types: ir.NewTypeTable(), Funcs: []*Function{{Name: "main", Body: &Block{Stmts: []Stmt{&Binding{Name: "value"}}}}}}
	want := "; hir module test\nfn main() {\n  let value\n}\n"
	if got := module.Text(); got != want {
		t.Fatalf("module text = %q, want %q", got, want)
	}
}
