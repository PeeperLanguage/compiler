package graph

import (
	"reflect"
	"testing"
)

type testDirectedEdge struct {
	from string
	to   string
	kind int
}

func TestDirectedOwnsBothAdjacencyDirections(t *testing.T) {
	g := NewDirected(func(edge testDirectedEdge) (string, string) { return edge.from, edge.to })
	first := testDirectedEdge{from: "a", to: "b", kind: 1}
	second := testDirectedEdge{from: "a", to: "b", kind: 2}
	if !g.AddEdge(first) || !g.AddEdge(second) || g.AddEdge(first) {
		t.Fatal("edge identity should preserve semantic metadata and reject exact duplicates")
	}
	if got := g.OutEdges("a"); !reflect.DeepEqual(got, []testDirectedEdge{first, second}) {
		t.Fatalf("out edges = %#v", got)
	}
	if got := g.InEdges("b"); !reflect.DeepEqual(got, []testDirectedEdge{first, second}) {
		t.Fatalf("in edges = %#v", got)
	}
}

func TestDirectedAlgorithmsShareCanonicalAdjacency(t *testing.T) {
	g := NewDirected(func(edge testDirectedEdge) (string, string) { return edge.from, edge.to })
	g.AddEdge(testDirectedEdge{from: "a", to: "b", kind: 1})
	g.AddEdge(testDirectedEdge{from: "b", to: "c", kind: 1})
	g.AddEdge(testDirectedEdge{from: "x", to: "y", kind: 2})
	includeOne := func(edge testDirectedEdge) bool { return edge.kind == 1 }

	order, cycles := g.TopoSort([]string{"a", "b", "c"}, includeOne)
	if len(cycles) != 0 || !reflect.DeepEqual(order, []string{"c", "b", "a"}) {
		t.Fatalf("topological result = %v, cycles = %v", order, cycles)
	}
	components := g.WeaklyConnectedComponents([]string{"a", "b", "c", "x", "y"}, includeOne)
	if !reflect.DeepEqual(components, [][]string{{"a", "b", "c"}, {"x"}, {"y"}}) {
		t.Fatalf("components = %#v", components)
	}
}
