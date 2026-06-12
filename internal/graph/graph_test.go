package graph

import (
	"slices"
	"testing"
)

func TestTopoSortOrdersImportDependencies(t *testing.T) {
	g := New()
	g.AddNode("a", Node{Kind: NodeModule})
	g.AddNode("b", Node{Kind: NodeModule})
	g.AddNode("c", Node{Kind: NodeModule})
	g.AddEdge("a", "b", EdgeImport)
	g.AddEdge("b", "c", EdgeImport)

	order, cycles := g.TopoSort([]NodeID{"a", "b", "c"}, EdgeImport)
	if len(cycles) != 0 {
		t.Fatalf("unexpected cycles: %v", cycles)
	}
	if slices.Index(order, "c") > slices.Index(order, "b") || slices.Index(order, "b") > slices.Index(order, "a") {
		t.Fatalf("unexpected order: %v", order)
	}
}

func TestTopoSortReportsCycles(t *testing.T) {
	g := New()
	g.AddNode("a", Node{Kind: NodeModule})
	g.AddNode("b", Node{Kind: NodeModule})
	g.AddEdge("a", "b", EdgeImport)
	g.AddEdge("b", "a", EdgeImport)

	_, cycles := g.TopoSort([]NodeID{"a", "b"}, EdgeImport)
	if len(cycles) == 0 {
		t.Fatalf("expected cycle, got none")
	}
}
