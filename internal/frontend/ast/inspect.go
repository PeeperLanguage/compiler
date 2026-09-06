package ast

import "compiler/pkg/typednil"

// Inspect traverses the AST in depth-first order: it starts by calling f(node);
// if f returns true, Inspect invokes f recursively for each of the non-nil children of node,
// followed by a call to f(nil).
func Inspect(node Node, f func(Node) bool) {
	if typednil.IsNil(node) {
		return
	}
	if !f(node) {
		return
	}

	node.forEachChild(func(child Node) { Inspect(child, f) })

	f(nil)
}

// Index returns every source node by its stable parser-assigned identity.
func Index(module *Module) map[NodeID]Node {
	nodes := make(map[NodeID]Node)
	if module == nil {
		return nodes
	}
	for _, stmt := range module.Stmts {
		Inspect(stmt, func(node Node) bool {
			if node != nil {
				nodes[node.ID()] = node
			}
			return true
		})
	}
	return nodes
}
