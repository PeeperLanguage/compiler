package graph

// Worklist is the canonical FIFO scheduler for fixed-point and graph analyses.
// It deduplicates pending nodes while allowing a node to be scheduled again
// after it has been processed and new information reaches it.
type Worklist[Node comparable] struct {
	queue  []Node
	queued map[Node]struct{}
	next   int
}

func NewWorklist[Node comparable](initial ...Node) *Worklist[Node] {
	work := &Worklist[Node]{queued: make(map[Node]struct{}, len(initial))}
	for _, node := range initial {
		work.Add(node)
	}
	return work
}

// Add schedules node if it is not already pending.
func (w *Worklist[Node]) Add(node Node) bool {
	if w == nil {
		return false
	}
	if _, found := w.queued[node]; found {
		return false
	}
	w.queued[node] = struct{}{}
	w.queue = append(w.queue, node)
	return true
}

// Next returns the next pending node. Once returned, that node may be scheduled
// again if a transfer changes one of its inputs.
func (w *Worklist[Node]) Next() (Node, bool) {
	var zero Node
	if w == nil || w.next >= len(w.queue) {
		return zero, false
	}
	node := w.queue[w.next]
	w.next++
	delete(w.queued, node)
	if w.next == len(w.queue) {
		w.queue = w.queue[:0]
		w.next = 0
	} else if w.next > 64 && w.next*2 >= len(w.queue) {
		w.queue = append(w.queue[:0], w.queue[w.next:]...)
		w.next = 0
	}
	return node, true
}
