package graph

import (
	"slices"
	"testing"
)

const (
	testNodeModule NodeKind = "module"
	testEdgeImport EdgeKind = "import"
)

func TestTopoSortOrdersImportDependencies(t *testing.T) {
	g := New(testNodeModule, testEdgeImport)
	g.AddNode("a")
	g.AddNode("b")
	g.AddNode("c")
	g.AddEdge("a", "b")
	g.AddEdge("b", "c")

	order, cycles := g.TopoSort([]NodeID{"a", "b", "c"})
	if len(cycles) != 0 {
		t.Fatalf("unexpected cycles: %v", cycles)
	}
	if slices.Index(order, "c") > slices.Index(order, "b") || slices.Index(order, "b") > slices.Index(order, "a") {
		t.Fatalf("unexpected order: %v", order)
	}
}

func TestTopoSortReportsCycles(t *testing.T) {
	g := New(testNodeModule, testEdgeImport)
	g.AddNode("a")
	g.AddNode("b")
	g.AddEdge("a", "b")
	g.AddEdge("b", "a")

	_, cycles := g.TopoSort([]NodeID{"a", "b"})
	if len(cycles) == 0 {
		t.Fatalf("expected cycle, got none")
	}
}

func TestGraphDegreeAndPredecessorQueries(t *testing.T) {
	g := New(testNodeModule, testEdgeImport)
	g.AddNode("a")
	g.AddNode("b")
	g.AddNode("c")
	g.AddEdge("a", "b")
	g.AddEdge("c", "b")

	if got := g.OutDegree("a"); got != 1 {
		t.Fatalf("unexpected out degree: %d", got)
	}
	if got := g.InDegree("b"); got != 2 {
		t.Fatalf("unexpected in degree: %d", got)
	}
	preds := g.Predecessors("b")
	if !slices.Contains(preds, NodeID("a")) || !slices.Contains(preds, NodeID("c")) {
		t.Fatalf("unexpected predecessors: %v", preds)
	}
}

func TestWeaklyConnectedComponents(t *testing.T) {
	g := New(testNodeModule, testEdgeImport)
	g.AddNode("a")
	g.AddNode("b")
	g.AddNode("c")
	g.AddNode("d")
	g.AddEdge("a", "b")
	g.AddEdge("c", "d")

	components := g.WeaklyConnectedComponents([]NodeID{"a", "b", "c", "d"})
	if len(components) != 2 {
		t.Fatalf("components = %d, want 2", len(components))
	}
	if !(slices.Contains(components[0], NodeID("a")) && slices.Contains(components[0], NodeID("b")) ||
		slices.Contains(components[1], NodeID("a")) && slices.Contains(components[1], NodeID("b"))) {
		t.Fatalf("missing {a,b} component: %v", components)
	}
}
