package graph

import "slices"

// Directed is the canonical directed-graph storage kernel. It owns ordered
// outgoing and incoming edge indexes once; domain graphs keep their semantic
// edge types and layer policy on top of this structure instead of maintaining
// private adjacency stores.
//
// Directed is intentionally not synchronized. Long-lived shared graphs may
// protect it with their own lock, while phase-local graphs such as CFGs avoid
// synchronization they do not need.
type Directed[Node comparable, Edge comparable] struct {
	endpoints func(Edge) (Node, Node)
	out       map[Node][]Edge
	in        map[Node][]Edge
}

func NewDirected[Node comparable, Edge comparable](endpoints func(Edge) (Node, Node)) *Directed[Node, Edge] {
	if endpoints == nil {
		panic("graph: Directed requires an edge endpoint function")
	}
	return &Directed[Node, Edge]{
		endpoints: endpoints,
		out:       make(map[Node][]Edge),
		in:        make(map[Node][]Edge),
	}
}

// AddEdge inserts edge once. Edge identity belongs to the domain edge value;
// two edges with the same endpoints but different semantic metadata are kept.
func (g *Directed[Node, Edge]) AddEdge(edge Edge) bool {
	if g == nil {
		return false
	}
	from, to := g.endpoints(edge)
	if slices.Contains(g.out[from], edge) {
		return false
	}
	g.out[from] = append(g.out[from], edge)
	g.in[to] = append(g.in[to], edge)
	return true
}

// Edges returns a snapshot of every stored edge, including disconnected edges
// whose endpoints a domain validator may not recognize. Source order is
// unspecified; within a source, insertion order is preserved.
func (g *Directed[Node, Edge]) Edges() []Edge {
	if g == nil {
		return nil
	}
	var edges []Edge
	for _, outgoing := range g.out {
		edges = append(edges, outgoing...)
	}
	return edges
}

// OutEdges returns outgoing edges in insertion order. The returned slice is a
// snapshot so consumers cannot corrupt the graph's reverse index accidentally.
func (g *Directed[Node, Edge]) OutEdges(id Node) []Edge {
	if g == nil {
		return nil
	}
	return append([]Edge(nil), g.out[id]...)
}

// InEdges returns incoming edges in insertion order.
func (g *Directed[Node, Edge]) InEdges(id Node) []Edge {
	if g == nil {
		return nil
	}
	return append([]Edge(nil), g.in[id]...)
}

func (g *Directed[Node, Edge]) Successors(id Node, include func(Edge) bool) []Node {
	if g == nil {
		return nil
	}
	result := make([]Node, 0, len(g.out[id]))
	for _, edge := range g.out[id] {
		if include != nil && !include(edge) {
			continue
		}
		_, to := g.endpoints(edge)
		result = append(result, to)
	}
	return result
}

func (g *Directed[Node, Edge]) Predecessors(id Node, include func(Edge) bool) []Node {
	if g == nil {
		return nil
	}
	result := make([]Node, 0, len(g.in[id]))
	for _, edge := range g.in[id] {
		if include != nil && !include(edge) {
			continue
		}
		from, _ := g.endpoints(edge)
		result = append(result, from)
	}
	return result
}

func (g *Directed[Node, Edge]) OutDegree(id Node, include func(Edge) bool) int {
	if g == nil {
		return 0
	}
	return countEdges(g.out[id], include)
}

func (g *Directed[Node, Edge]) InDegree(id Node, include func(Edge) bool) int {
	if g == nil {
		return 0
	}
	return countEdges(g.in[id], include)
}

func (g *Directed[Node, Edge]) TopoSort(ids []Node, include func(Edge) bool) ([]Node, [][]Node) {
	if g == nil || len(ids) == 0 {
		return nil, nil
	}
	index := make(map[Node]struct{}, len(ids))
	for _, id := range ids {
		index[id] = struct{}{}
	}

	const (
		visitNone = iota
		visitTemp
		visitDone
	)
	state := make(map[Node]uint8, len(index))
	order := make([]Node, 0, len(index))
	stack := make([]Node, 0, len(index))
	cycles := make([][]Node, 0)

	var visit func(Node)
	visit = func(id Node) {
		switch state[id] {
		case visitTemp:
			cycles = append(cycles, extractDirectedCycle(stack, id))
			return
		case visitDone:
			return
		}
		state[id] = visitTemp
		stack = append(stack, id)
		for _, next := range g.Successors(id, include) {
			if _, ok := index[next]; ok {
				visit(next)
			}
		}
		stack = stack[:len(stack)-1]
		state[id] = visitDone
		order = append(order, id)
	}

	for _, id := range ids {
		visit(id)
	}
	return order, cycles
}

func (g *Directed[Node, Edge]) WeaklyConnectedComponents(ids []Node, include func(Edge) bool) [][]Node {
	if g == nil || len(ids) == 0 {
		return nil
	}
	index := make(map[Node]struct{}, len(ids))
	for _, id := range ids {
		index[id] = struct{}{}
	}
	visited := make(map[Node]struct{}, len(index))
	components := make([][]Node, 0)
	for _, start := range ids {
		if _, ok := visited[start]; ok {
			continue
		}
		queue := []Node{start}
		visited[start] = struct{}{}
		component := make([]Node, 0)
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			component = append(component, current)
			neighbors := g.Successors(current, include)
			neighbors = append(neighbors, g.Predecessors(current, include)...)
			for _, next := range neighbors {
				if _, ok := index[next]; !ok {
					continue
				}
				if _, ok := visited[next]; ok {
					continue
				}
				visited[next] = struct{}{}
				queue = append(queue, next)
			}
		}
		components = append(components, component)
	}
	return components
}

func countEdges[Edge comparable](edges []Edge, include func(Edge) bool) int {
	if include == nil {
		return len(edges)
	}
	total := 0
	for _, edge := range edges {
		if include(edge) {
			total++
		}
	}
	return total
}

func extractDirectedCycle[Node comparable](stack []Node, target Node) []Node {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == target {
			cycle := append([]Node(nil), stack[i:]...)
			cycle = append(cycle, target)
			return cycle
		}
	}
	return []Node{target}
}
