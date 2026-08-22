package ir

// InspectExpr traverses an expression in depth-first preorder. Returning false
// from visit skips that expression's children.
func InspectExpr(expr Expr, visit func(Expr) bool) {
	if expr == nil || !visit(expr) {
		return
	}
	expr.forEachChild(func(child Expr) { InspectExpr(child, visit) })
}

// InspectPlace traverses expressions that determine a place: root first, then
// projection indexes in storage order.
func InspectPlace(place *Place, visit func(Expr) bool) {
	if place == nil {
		return
	}
	place.forEachChild(func(child Expr) { InspectExpr(child, visit) })
}
