package graph

import (
	"slices"
	"sync"
)

type NodeID string

type EdgeKind string

type edge struct {
	from NodeID
	to   NodeID
	kind EdgeKind
}

type Graph struct {
	mu       sync.RWMutex
	edgeKind EdgeKind
	directed *Directed[NodeID, edge]
}

func New(edgeKind EdgeKind) *Graph {
	return &Graph{
		edgeKind: edgeKind,
		directed: NewDirected(func(edge edge) (NodeID, NodeID) { return edge.from, edge.to }),
	}
}

func (g *Graph) AddEdge(from, to NodeID, kinds ...EdgeKind) {
	if g == nil || from == "" || to == "" {
		return
	}
	kind := g.edgeKind
	if len(kinds) > 0 {
		kind = kinds[0]
	}
	if kind == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.directed.AddEdge(edge{from: from, to: to, kind: kind})
}

func (g *Graph) Successors(id NodeID, kinds ...EdgeKind) []NodeID {
	if g == nil || id == "" {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.directed.Successors(id, g.edgeFilter(kinds))
}

func (g *Graph) Predecessors(id NodeID, kinds ...EdgeKind) []NodeID {
	if g == nil || id == "" {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.directed.Predecessors(id, g.edgeFilter(kinds))
}

func (g *Graph) OutDegree(id NodeID, kinds ...EdgeKind) int {
	if g == nil || id == "" {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.directed.OutDegree(id, g.edgeFilter(kinds))
}

func (g *Graph) InDegree(id NodeID, kinds ...EdgeKind) int {
	if g == nil || id == "" {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.directed.InDegree(id, g.edgeFilter(kinds))
}

func (g *Graph) TopoSort(ids []NodeID, kinds ...EdgeKind) ([]NodeID, [][]NodeID) {
	if g == nil || len(ids) == 0 {
		return nil, nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.directed.TopoSort(nonEmptyNodeIDs(ids), g.edgeFilter(kinds))
}

func (g *Graph) WeaklyConnectedComponents(ids []NodeID, kinds ...EdgeKind) [][]NodeID {
	if g == nil || len(ids) == 0 {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.directed.WeaklyConnectedComponents(nonEmptyNodeIDs(ids), g.edgeFilter(kinds))
}

// Empty IDs are invalid only in the domain facade, not in Directed.
func nonEmptyNodeIDs(ids []NodeID) []NodeID {
	first := slices.Index(ids, NodeID(""))
	if first < 0 {
		return ids
	}
	filtered := make([]NodeID, first, len(ids)-1)
	copy(filtered, ids[:first])
	for _, id := range ids[first+1:] {
		if id != "" {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

func (g *Graph) edgeFilter(kinds []EdgeKind) func(edge) bool {
	allowed := kindSet(kinds, g.edgeKind)
	if len(allowed) == 0 {
		return nil
	}
	return func(candidate edge) bool {
		_, ok := allowed[candidate.kind]
		return ok
	}
}

func kindSet(kinds []EdgeKind, defaultKind EdgeKind) map[EdgeKind]struct{} {
	if len(kinds) == 0 {
		if defaultKind == "" {
			return nil
		}
		return map[EdgeKind]struct{}{defaultKind: {}}
	}
	allowed := make(map[EdgeKind]struct{}, len(kinds))
	for _, kind := range kinds {
		if kind != "" {
			allowed[kind] = struct{}{}
		}
	}
	return allowed
}
