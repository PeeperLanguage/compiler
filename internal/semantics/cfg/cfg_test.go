package cfg

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/hir"
)

func TestBlockCreation(t *testing.T) {
	b := &Block{ID: 1, Reachable: true}
	if b.ID != 1 {
		t.Fatalf("expected block ID 1, got %d", b.ID)
	}
	if !b.Reachable {
		t.Fatalf("expected block to be reachable")
	}
}

func TestGraphCreation(t *testing.T) {
	g := &Graph{Name: "test"}
	if g.Name != "test" {
		t.Fatalf("expected graph name 'test', got %s", g.Name)
	}
}

func TestBuildModulePreservesLexicalScopeExits(t *testing.T) {
	types := ir.NewTypeTable()
	void := types.Intern(ir.Type{Kind: ir.TypeVoid})
	mod := &hir.Module{Types: types, Funcs: []*hir.Function{{
		Name:       "main",
		ReturnType: void,
		Body: &hir.Block{NodeID: 10, Stmts: []hir.Stmt{
			&hir.Block{NodeID: 20},
		}},
	}}}

	graphs := BuildModule(mod)
	if len(graphs) != 1 || graphs[0] == nil || graphs[0].Entry == nil {
		t.Fatalf("CFG = %#v, want one graph with entry", graphs)
	}
	if got := graphs[0].Entry.ScopeExits; len(got) != 1 || got[0] != 20 {
		t.Fatalf("entry scope exits = %v, want nested scope", got)
	}
	if len(graphs[0].Blocks) < 3 {
		t.Fatalf("CFG blocks = %#v, want nested continuation", graphs[0].Blocks)
	}
	continuation := graphs[0].Blocks[2]
	if got := continuation.ScopeExits; len(got) != 1 || got[0] != 10 {
		t.Fatalf("continuation scope exits = %v, want function body", got)
	}
}

func TestBuildModulePreservesTerminatorSourceIdentity(t *testing.T) {
	types := ir.NewTypeTable()
	void := types.Intern(ir.Type{Kind: ir.TypeVoid})
	boolType := types.Intern(ir.Type{Kind: ir.TypeBool})
	mod := &hir.Module{Types: types, Funcs: []*hir.Function{{
		Name:       "main",
		ReturnType: void,
		Body: &hir.Block{Stmts: []hir.Stmt{
			&hir.If{NodeID: 30, Cond: &ir.BoolLit{Value: true, Type: boolType}, Then: &hir.Block{}},
			&hir.Return{NodeID: 40},
		}},
	}}}

	graph := BuildModule(mod)[0]
	branch, ok := graph.Entry.Terminator.(*Branch)
	if !ok || branch.NodeID != 30 {
		t.Fatalf("branch = %#v, want source node 30", graph.Entry.Terminator)
	}
	var ret *Return
	for _, block := range graph.Blocks {
		if candidate, ok := block.Terminator.(*Return); ok {
			ret = candidate
			break
		}
	}
	if ret == nil || ret.NodeID != 40 {
		t.Fatalf("return = %#v, want source node 40", ret)
	}
}
