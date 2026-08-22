package ast

// Inspect traverses the AST in depth-first order: it starts by calling f(node);
// if f returns true, Inspect invokes f recursively for each of the non-nil children of node,
// followed by a call to f(nil).
func Inspect(node Node, f func(Node) bool) {
	if node == nil || IsNilNode(node) {
		return
	}
	if !f(node) {
		return
	}

	node.forEachChild(func(child Node) { Inspect(child, f) })

	f(nil)
}
