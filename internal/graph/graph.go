package graph

import "sync"

type NodeID string

type NodeKind uint8

const (
	NodeUnknown NodeKind = iota
	NodeModule
)

type EdgeKind uint8

const (
	EdgeUnknown EdgeKind = iota
	EdgeImport
)

type Node struct {
	Kind NodeKind
}

type Graph struct {
	mu    sync.RWMutex
	nodes map[NodeID]Node
	out   map[NodeID]map[EdgeKind]map[NodeID]struct{}
	in    map[NodeID]map[EdgeKind]map[NodeID]struct{}
}

func New() *Graph {
	return &Graph{
		nodes: make(map[NodeID]Node),
		out:   make(map[NodeID]map[EdgeKind]map[NodeID]struct{}),
		in:    make(map[NodeID]map[EdgeKind]map[NodeID]struct{}),
	}
}

func (g *Graph) AddNode(id NodeID, node Node) {
	if g == nil || id == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodes[id] = node
}

func (g *Graph) AddEdge(from, to NodeID, kind EdgeKind) {
	if g == nil || from == "" || to == "" || kind == EdgeUnknown {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.addEdgeLocked(g.out, from, kind, to)
	g.addEdgeLocked(g.in, to, kind, from)
}

func (g *Graph) Successors(id NodeID, kinds ...EdgeKind) []NodeID {
	if g == nil || id == "" {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return successorSnapshot(g.out[id], kindSet(kinds))
}

func (g *Graph) TopoSort(ids []NodeID, kinds ...EdgeKind) ([]NodeID, [][]NodeID) {
	if g == nil || len(ids) == 0 {
		return nil, nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	index := make(map[NodeID]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			index[id] = struct{}{}
		}
	}
	if len(index) == 0 {
		return nil, nil
	}

	const (
		visitNone = iota
		visitTemp
		visitDone
	)

	state := make(map[NodeID]uint8, len(index))
	order := make([]NodeID, 0, len(index))
	stack := make([]NodeID, 0, len(index))
	cycles := make([][]NodeID, 0)
	allowedKinds := kindSet(kinds)

	var visit func(NodeID)
	visit = func(id NodeID) {
		switch state[id] {
		case visitTemp:
			cycles = append(cycles, extractCycle(stack, id))
			return
		case visitDone:
			return
		}
		state[id] = visitTemp
		stack = append(stack, id)
		for _, next := range successorSnapshot(g.out[id], allowedKinds) {
			if _, ok := index[next]; ok {
				visit(next)
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = visitDone
		order = append(order, id)
	}

	for _, id := range ids {
		if id != "" {
			visit(id)
		}
	}

	return order, cycles
}

func (g *Graph) addEdgeLocked(index map[NodeID]map[EdgeKind]map[NodeID]struct{}, from NodeID, kind EdgeKind, to NodeID) {
	edgesByKind, ok := index[from]
	if !ok {
		edgesByKind = make(map[EdgeKind]map[NodeID]struct{})
		index[from] = edgesByKind
	}
	edges, ok := edgesByKind[kind]
	if !ok {
		edges = make(map[NodeID]struct{})
		edgesByKind[kind] = edges
	}
	edges[to] = struct{}{}
}

func kindSet(kinds []EdgeKind) map[EdgeKind]struct{} {
	if len(kinds) == 0 {
		return nil
	}
	allowed := make(map[EdgeKind]struct{}, len(kinds))
	for _, kind := range kinds {
		if kind != EdgeUnknown {
			allowed[kind] = struct{}{}
		}
	}
	return allowed
}

func successorSnapshot(edgesByKind map[EdgeKind]map[NodeID]struct{}, allowed map[EdgeKind]struct{}) []NodeID {
	if len(edgesByKind) == 0 {
		return nil
	}
	result := make([]NodeID, 0)
	for kind, edges := range edgesByKind {
		if len(allowed) > 0 {
			if _, ok := allowed[kind]; !ok {
				continue
			}
		}
		for id := range edges {
			result = append(result, id)
		}
	}
	return result
}

func extractCycle(stack []NodeID, target NodeID) []NodeID {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == target {
			cycle := append([]NodeID{}, stack[i:]...)
			cycle = append(cycle, target)
			return cycle
		}
	}
	return []NodeID{target}
}
