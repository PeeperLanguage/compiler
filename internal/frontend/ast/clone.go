package ast

import "sync/atomic"

var nextSyntheticNodeID atomic.Uint32

// SubstituteExpr clones an expression for call-site expansion. Parameter
// identifiers are replaced with their already-evaluated argument expressions;
// every cloned node gets a separate high-range ID so semantic caches cannot
// collide with parser-assigned nodes.
//
// Clone logic lives on each expression type via the Expr.copyExpr interface
// method. Adding a new Expr type that is missing copyExpr produces a compile
// error, so there is no silent default fallthrough.
func SubstituteExpr(expr Expr, substitutions map[string]Expr) (Expr, map[NodeID]NodeID) {
	if expr == nil {
		return nil, nil
	}
	clonedIDs := make(map[NodeID]NodeID)
	cloneID := func() NodeID {
		return NodeID(nextSyntheticNodeID.Add(1) | (1 << 31))
	}
	newID := func(original NodeID) NodeID {
		id := cloneID()
		clonedIDs[original] = id
		return id
	}
	cloned := expr.copyExpr(substitutions, newID, &clonedIDs)
	return cloned, clonedIDs
}
