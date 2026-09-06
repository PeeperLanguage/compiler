package graph

import (
	"fmt"
	"reflect"
	"testing"
)

type testDirectedEdge struct {
	from string
	to   string
	kind int
}

func TestDirectedPreservesZeroNode(t *testing.T) {
	g := NewDirected(func(edge [2]int) (int, int) { return edge[0], edge[1] })
	g.AddEdge([2]int{1, 0})
	order, cycles := g.TopoSort([]int{1, 0}, nil)
	if !reflect.DeepEqual(order, []int{0, 1}) || len(cycles) != 0 {
		t.Fatalf("topology = %v, %v; want [0 1], no cycles", order, cycles)
	}
	if got := g.WeaklyConnectedComponents([]int{1, 0}, nil); !reflect.DeepEqual(got, [][]int{{1, 0}}) {
		t.Fatalf("components = %v; want [[1 0]]", got)
	}
	if got := g.InEdges(0); !reflect.DeepEqual(got, [][2]int{{1, 0}}) {
		t.Fatalf("zero node incoming edges = %v", got)
	}
}

func BenchmarkDirectedConstruction(b *testing.B) {
	for _, shape := range []struct {
		name    string
		sources int
		degree  int
	}{
		{name: "cfg", sources: 1024, degree: 2},
		{name: "dependencies", sources: 256, degree: 8},
		{name: "fanout", sources: 1, degree: 128},
		{name: "fanout", sources: 1, degree: 1024},
		{name: "fanout", sources: 1, degree: 4096},
		{name: "fanout", sources: 1, degree: 16384},
	} {
		b.Run(fmt.Sprintf("%s/%d", shape.name, shape.degree), func(b *testing.B) {
			edges := make([][2]int, 0, shape.sources*shape.degree)
			for from := 0; from < shape.sources; from++ {
				for offset := 1; offset <= shape.degree; offset++ {
					edges = append(edges, [2]int{from, from + offset})
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				g := NewDirected(func(edge [2]int) (int, int) { return edge[0], edge[1] })
				for _, edge := range edges {
					g.AddEdge(edge)
				}
			}
		})
	}
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

func TestDirectedEdgesSnapshot(t *testing.T) {
	var missing *Directed[string, testDirectedEdge]
	if len(missing.Edges()) != 0 {
		t.Fatal("nil graph has edges")
	}
	g := NewDirected(func(edge testDirectedEdge) (string, string) { return edge.from, edge.to })
	want := map[testDirectedEdge]bool{
		{from: "a", to: "b", kind: 1}:                  true,
		{from: "a", to: "b", kind: 2}:                  true,
		{from: "foreign", to: "disconnected", kind: 1}: true,
	}
	for edge := range want {
		g.AddEdge(edge)
	}
	snapshot := g.Edges()
	if len(snapshot) != len(want) {
		t.Fatalf("snapshot = %v, want %v", snapshot, want)
	}
	for _, edge := range snapshot {
		if !want[edge] {
			t.Fatalf("unexpected or repeated edge %v", edge)
		}
		delete(want, edge)
	}
	original := snapshot[0]
	snapshot[0] = testDirectedEdge{}
	found := false
	for _, edge := range g.OutEdges(original.from) {
		found = found || edge == original
	}
	if !found {
		t.Fatal("snapshot mutation changed canonical adjacency")
	}
	g.AddEdge(testDirectedEdge{from: "new", to: "node"})
	if len(snapshot) != 3 {
		t.Fatal("later graph mutation changed snapshot")
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
