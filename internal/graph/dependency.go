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

// DependencyGraph is the synchronized graph for dependency relationships.
// An edge points from a dependent to the node it requires.
type DependencyGraph struct {
	mu       sync.RWMutex
	edgeKind EdgeKind
	directed *Directed[NodeID, edge]
}

func NewDependencyGraph(edgeKind EdgeKind) *DependencyGraph {
	return &DependencyGraph{
		edgeKind: edgeKind,
		directed: NewDirected(func(edge edge) (NodeID, NodeID) { return edge.from, edge.to }),
	}
}

func (g *DependencyGraph) AddEdge(from, to NodeID, kinds ...EdgeKind) {
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

// TransitiveDependents returns every node that directly or indirectly depends
// on one of roots. Roots are excluded, including when a dependency cycle leads
// back to one, and each dependent appears once in breadth-first discovery order.
func (g *DependencyGraph) TransitiveDependents(roots []NodeID, kinds ...EdgeKind) []NodeID {
	if g == nil || len(roots) == 0 {
		return nil
	}
	roots = nonEmptyNodeIDs(roots)
	if len(roots) == 0 {
		return nil
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	seen := make(map[NodeID]struct{}, len(roots))
	work := NewWorklist[NodeID]()
	for _, root := range roots {
		seen[root] = struct{}{}
		work.Add(root)
	}

	var dependents []NodeID
	include := g.edgeFilter(kinds)
	for {
		current, pending := work.Next()
		if !pending {
			break
		}
		for _, dependent := range g.directed.Predecessors(current, include) {
			if _, found := seen[dependent]; found {
				continue
			}
			seen[dependent] = struct{}{}
			dependents = append(dependents, dependent)
			work.Add(dependent)
		}
	}
	return dependents
}

func (g *DependencyGraph) InDegree(id NodeID, kinds ...EdgeKind) int {
	if g == nil || id == "" {
		return 0
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.directed.InDegree(id, g.edgeFilter(kinds))
}

func (g *DependencyGraph) TopoSort(ids []NodeID, kinds ...EdgeKind) ([]NodeID, [][]NodeID) {
	if g == nil || len(ids) == 0 {
		return nil, nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.directed.TopoSort(nonEmptyNodeIDs(ids), g.edgeFilter(kinds))
}

func (g *DependencyGraph) WeaklyConnectedComponents(ids []NodeID, kinds ...EdgeKind) [][]NodeID {
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

func (g *DependencyGraph) edgeFilter(kinds []EdgeKind) func(edge) bool {
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
