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

func TestGraphDegreeAndPredecessorQueries(t *testing.T) {
	g := New()
	g.AddNode("a", Node{Kind: NodeModule})
	g.AddNode("b", Node{Kind: NodeModule})
	g.AddNode("c", Node{Kind: NodeModule})
	g.AddEdge("a", "b", EdgeImport)
	g.AddEdge("c", "b", EdgeImport)

	if got := g.OutDegree("a", EdgeImport); got != 1 {
		t.Fatalf("unexpected out degree: %d", got)
	}
	if got := g.InDegree("b", EdgeImport); got != 2 {
		t.Fatalf("unexpected in degree: %d", got)
	}
	preds := g.Predecessors("b", EdgeImport)
	if !slices.Contains(preds, NodeID("a")) || !slices.Contains(preds, NodeID("c")) {
		t.Fatalf("unexpected predecessors: %v", preds)
	}
}
