package graph

import (
	"reflect"
	"slices"
	"testing"
)

const (
	testEdgeImport   EdgeKind = "import"
	testEdgeMetadata EdgeKind = "metadata"
)

func TestAlgorithmsIgnoreEmptyNodeIDs(t *testing.T) {
	g := New(testEdgeImport)
	g.AddEdge("a", "b")
	g.AddEdge("b", "a", testEdgeMetadata)
	for _, tc := range []struct {
		name  string
		ids   []NodeID
		kinds []EdgeKind
	}{
		{name: "nil"},
		{name: "empty", ids: []NodeID{}},
		{name: "all empty", ids: []NodeID{"", ""}},
		{name: "mixed duplicates isolated", ids: []NodeID{"", "a", "", "b", "a", "isolated", ""}},
		{name: "metadata", ids: []NodeID{"", "a", "b", "isolated", ""}, kinds: []EdgeKind{testEdgeMetadata}},
		{name: "cycle", ids: []NodeID{"", "a", "b", "a", ""}, kinds: []EdgeKind{testEdgeImport, testEdgeMetadata}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := slices.Clone(tc.ids)
			var valid []NodeID
			for _, id := range tc.ids {
				if id != "" {
					valid = append(valid, id)
				}
			}
			wantOrder, wantCycles := g.TopoSort(valid, tc.kinds...)
			order, cycles := g.TopoSort(tc.ids, tc.kinds...)
			if !reflect.DeepEqual(order, wantOrder) || !reflect.DeepEqual(cycles, wantCycles) {
				t.Errorf("topology = %v, %v; want %v, %v", order, cycles, wantOrder, wantCycles)
			}
			wantComponents := g.WeaklyConnectedComponents(valid, tc.kinds...)
			if got := g.WeaklyConnectedComponents(tc.ids, tc.kinds...); !reflect.DeepEqual(got, wantComponents) {
				t.Errorf("components = %v; want %v", got, wantComponents)
			}
			if !reflect.DeepEqual(tc.ids, original) {
				t.Errorf("caller IDs changed: %v; want %v", tc.ids, original)
			}
		})
	}
}

func TestTopoSortOrdersImportDependencies(t *testing.T) {
	g := New(testEdgeImport)
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
	g := New(testEdgeImport)
	g.AddEdge("a", "b")
	g.AddEdge("b", "a")

	_, cycles := g.TopoSort([]NodeID{"a", "b"})
	if len(cycles) == 0 {
		t.Fatalf("expected cycle, got none")
	}
}

func TestGraphDegreeAndPredecessorQueries(t *testing.T) {
	g := New(testEdgeImport)
	g.AddEdge("a", "b")
	g.AddEdge("c", "b")
	g.AddEdge("a", "c", testEdgeMetadata)

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
	if got := g.Successors("a", testEdgeMetadata); !slices.Equal(got, []NodeID{"c"}) {
		t.Fatalf("metadata successors = %v, want [c]", got)
	}
	if got := g.OutDegree("a", testEdgeMetadata); got != 1 {
		t.Fatalf("metadata out degree = %d, want 1", got)
	}
}

func TestWeaklyConnectedComponents(t *testing.T) {
	g := New(testEdgeImport)
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

func TestAlgorithmsPreserveCallerProvidedIsolatedNodes(t *testing.T) {
	g := New(testEdgeImport)
	ids := []NodeID{"connected", "dependency", "isolated"}
	g.AddEdge("connected", "dependency")

	order, cycles := g.TopoSort(ids)
	if len(cycles) != 0 {
		t.Fatalf("unexpected cycles: %v", cycles)
	}
	if len(order) != len(ids) || !slices.Contains(order, NodeID("isolated")) {
		t.Fatalf("topological order lost isolated node: %v", order)
	}

	components := g.WeaklyConnectedComponents(ids)
	if len(components) != 2 {
		t.Fatalf("components = %v, want connected pair plus isolated node", components)
	}
	foundIsolated := false
	for _, component := range components {
		if slices.Equal(component, []NodeID{"isolated"}) {
			foundIsolated = true
		}
	}
	if !foundIsolated {
		t.Fatalf("components lost isolated node: %v", components)
	}
}
