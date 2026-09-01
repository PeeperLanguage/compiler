package hir

import (
	"strings"
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

func TestInspectStmtTraversesStructuredChildren(t *testing.T) {
	root := &Block{NodeID: 1, Stmts: []Stmt{
		&Binding{NodeID: 2},
		&If{NodeID: 3, Then: &Block{NodeID: 4, Stmts: []Stmt{&Return{NodeID: 5}}}, Else: &Block{NodeID: 6}},
	}}
	got := make([]NodeID, 0)
	InspectStmt(root, func(stmt Stmt) bool {
		got = append(got, NodeIDOf(stmt))
		return true
	})
	want := []NodeID{1, 2, 3, 4, 5, 6}
	if len(got) != len(want) {
		t.Fatalf("visited NodeIDs = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("visited NodeIDs = %v, want %v", got, want)
		}
	}
}

func TestForHIRKeepsGeneratedSegmentsInTraversalAndText(t *testing.T) {
	loop := &For{
		Init:     &Block{Stmts: []Stmt{&Binding{Name: "cursor"}}},
		Cond:     &ir.Ident{Name: "more"},
		Bindings: &Block{Stmts: []Stmt{&Binding{Name: "value"}}},
		Body:     &Block{NodeID: 6, Stmts: []Stmt{&ExprStmt{Value: &ir.Ident{Name: "work"}, NodeID: 7}}},
		Next:     &Block{Stmts: []Stmt{&Assign{Target: &ir.Place{Root: &ir.Ident{Name: "cursor"}}, Value: &ir.Ident{Name: "next"}}}},
		NodeID:   1,
	}
	visited := make([]NodeID, 0)
	InspectStmt(loop, func(stmt Stmt) bool {
		visited = append(visited, NodeIDOf(stmt))
		return true
	})
	wantVisited := []NodeID{1, 0, 0, 0, 0, 6, 7, 0, 0}
	if len(visited) != len(wantVisited) {
		t.Fatalf("visited loop nodes = %v, want %v", visited, wantVisited)
	}
	for index := range wantVisited {
		if visited[index] != wantVisited[index] {
			t.Fatalf("visited loop nodes = %v, want %v", visited, wantVisited)
		}
	}
	var text strings.Builder
	loop.appendText(&text, 0)
	wantText := "for more {\n  init {\n    let cursor\n  }\n  bindings {\n    let value\n  }\n  work\n  next {\n    cursor = next\n  }\n}\n"
	if got := text.String(); got != wantText {
		t.Fatalf("loop text = %q, want %q", got, wantText)
	}
}

func TestSwitchVariantHIRKeepsCaseBlocksAndText(t *testing.T) {
	switchStmt := &SwitchVariant{
		Value: &ir.Ident{Name: "status"},
		Cases: []VariantCaseBlock{
			{Case: 0, Body: &Block{NodeID: 2}},
			{Case: 1, Body: &Block{NodeID: 3}},
		},
		NodeID: 1,
	}
	visited := make([]NodeID, 0)
	InspectStmt(switchStmt, func(stmt Stmt) bool {
		visited = append(visited, NodeIDOf(stmt))
		return true
	})
	var text strings.Builder
	switchStmt.appendText(&text, 0)
	if got := text.String(); got != "switch-variant status {\n  case 0 {\n  }\n  case 1 {\n  }\n}\n" {
		t.Fatalf("switch text = %q", got)
	}
	if len(visited) != 3 || visited[0] != 1 || visited[1] != 2 || visited[2] != 3 {
		t.Fatalf("visited switch nodes = %v", visited)
	}
}
