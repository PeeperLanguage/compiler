package cfg

import (
	"reflect"
	"testing"

	"compiler/internal/diagnostics"
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

func TestGraphContainsOnlyControlFlowArtifacts(t *testing.T) {
	if _, found := reflect.TypeOf(Graph{}).FieldByName("Cleanup"); found {
		t.Fatal("CFG graph retains ownership cleanup output")
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

func TestBuildModuleCreatesCanonicalSiteAdjacency(t *testing.T) {
	types := ir.NewTypeTable()
	void := types.Intern(ir.Type{Kind: ir.TypeVoid})
	boolType := types.Intern(ir.Type{Kind: ir.TypeBool})
	graph := BuildModule(&hir.Module{Types: types, Funcs: []*hir.Function{{
		Name:       "main",
		ReturnType: void,
		Body: &hir.Block{Stmts: []hir.Stmt{
			&hir.If{NodeID: 30, Cond: &ir.BoolLit{Value: true, Type: boolType}, Then: &hir.Block{}, Else: &hir.Block{}},
		}},
	}}})[0]
	if graph == nil || graph.Entry == nil || len(graph.Entry.Sites) != 1 {
		t.Fatalf("entry sites = %#v, want one branch site", graph)
	}
	branch := graph.Entry.Sites[0]
	if branch.Kind != SiteTerminator || branch.NodeID != 30 || len(branch.Successors) != 2 {
		t.Fatalf("branch site = %#v, want two branch successors", branch)
	}
	wantKinds := map[EdgeKind]bool{EdgeTrue: false, EdgeFalse: false}
	for _, successor := range branch.Successors {
		wantKinds[successor.Kind] = true
		block := graph.Blocks[successor.To.Block]
		site := block.Sites[successor.To.Index]
		if len(site.Predecessors) != 1 || site.Predecessors[0].From != branch.ID || site.Predecessors[0].Kind != successor.Kind {
			t.Fatalf("site %#v predecessors = %v, want branch %#v", site.ID, site.Predecessors, branch.ID)
		}
	}
	if !wantKinds[EdgeTrue] || !wantKinds[EdgeFalse] {
		t.Fatalf("branch edge kinds = %#v, want true and false", branch.Successors)
	}
}

func TestBuildModulePreservesDisconnectedStatementsAfterReturn(t *testing.T) {
	types := ir.NewTypeTable()
	void := types.Intern(ir.Type{Kind: ir.TypeVoid})
	graph := BuildModule(&hir.Module{Types: types, Funcs: []*hir.Function{{
		Name:       "main",
		ReturnType: void,
		Body: &hir.Block{Stmts: []hir.Stmt{
			&hir.Return{NodeID: 40},
			&hir.ExprStmt{NodeID: 41, Value: &ir.IntLit{Value: "1", Type: types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})}},
		}},
	}}})[0]
	found := false
	for _, block := range graph.Blocks {
		if block != nil && !block.Reachable && len(block.Stmts) == 1 && hir.NodeIDOf(block.Stmts[0]) == 41 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("CFG blocks = %#v, want disconnected statement after return", graph.Blocks)
	}
	diag := diagnostics.NewDiagnosticBag()
	if !Analyze([]*Graph{graph}, diag) {
		t.Fatal("void CFG with unreachable syntax must remain valid")
	}
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.WarnUnreachableCode {
			return
		}
	}
	t.Fatalf("diagnostics = %#v, want unreachable warning", diag.Diagnostics())
}

func TestAnalyzeDoesNotRebuildFinalizedTopology(t *testing.T) {
	types := ir.NewTypeTable()
	void := types.Intern(ir.Type{Kind: ir.TypeVoid})
	graph := BuildModule(&hir.Module{Types: types, Funcs: []*hir.Function{{
		Name: "main", ReturnType: void, Body: &hir.Block{},
	}}})[0]
	before := append([]*Block(nil), graph.Entry.Predecessors...)
	graph.Entry.Sites = nil
	Analyze([]*Graph{graph}, diagnostics.NewDiagnosticBag())
	if graph.Entry.Sites != nil || !reflect.DeepEqual(graph.Entry.Predecessors, before) {
		t.Fatalf("Analyze mutated finalized topology: entry = %#v", graph.Entry)
	}
}
