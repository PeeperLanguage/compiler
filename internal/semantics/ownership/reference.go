package ownership

import (
	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type referenceValue struct {
	origins []place.Origin
	mutable bool
	site    ast.Node
}

type referenceLiveSet map[*symbols.Symbol]ast.Node

func (a *analyzer) referenceValueForExpr(scope *table.Scope, expr ast.Expr, st state) (referenceValue, bool) {
	if a == nil || scope == nil || expr == nil {
		return referenceValue{}, false
	}
	_, mutable, ok := typeinfo.ReferenceValueTarget(a.exprType(expr))
	if !ok {
		return referenceValue{}, false
	}
	origins := place.Origins(scope, expr, place.OriginOptions{
		ExprType: a.exprType,
		ReferenceOrigins: func(sym *symbols.Symbol) []place.Origin {
			return st.references[sym].origins
		},
		ConstantIndex: func(index ast.Expr) (string, bool) {
			expected := a.exprType(index)
			if !typeinfo.IsIntegral(expected) {
				expected = typeinfo.DefaultIntegerType()
			}
			value, evaluated := consteval.EvaluateExpr(a.ctx, a.module, scope, index, expected)
			integer, integral := value.(*constvalue.IntConst)
			if !evaluated || !integral || integer == nil {
				return "", false
			}
			return integer.Value, true
		},
	})
	if len(origins) == 0 {
		return referenceValue{}, false
	}
	return referenceValue{origins: origins, mutable: mutable, site: expr}, true
}

func (a *analyzer) updateReferenceSymbol(sym *symbols.Symbol, value referenceValue, hasValue bool, st state) {
	if sym == nil {
		return
	}
	mutable, reference := referenceMutability(sym)
	if !reference || !hasValue {
		delete(st.references, sym)
		return
	}
	value.mutable = mutable
	value.origins = place.CloneOrigins(value.origins)
	st.references[sym] = value
}

func referenceMutability(sym *symbols.Symbol) (bool, bool) {
	typ, ok := symbols.GetSymbolType(sym)
	if !ok {
		return false, false
	}
	_, mutable, reference := typeinfo.ReferenceValueTarget(typ)
	return mutable, reference
}

func copyReferenceValue(value referenceValue) referenceValue {
	value.origins = place.CloneOrigins(value.origins)
	return value
}

func sameReferenceValues(left, right map[*symbols.Symbol]referenceValue) bool {
	if len(left) != len(right) {
		return false
	}
	for sym, leftValue := range left {
		rightValue, ok := right[sym]
		if !ok || leftValue.mutable != rightValue.mutable || leftValue.site != rightValue.site ||
			!place.SameOrigins(leftValue.origins, rightValue.origins) {
			return false
		}
	}
	return true
}

func mergeReferenceValues(dst, src map[*symbols.Symbol]referenceValue) bool {
	changed := false
	for sym, srcValue := range src {
		dstValue, exists := dst[sym]
		if !exists {
			dst[sym] = copyReferenceValue(srcValue)
			changed = true
			continue
		}
		merged := place.MergeOrigins(dstValue.origins, srcValue.origins)
		if !place.SameOrigins(dstValue.origins, merged) {
			dstValue.origins = merged
			changed = true
		}
		if earlierNode(srcValue.site, dstValue.site) == srcValue.site && dstValue.site != srcValue.site {
			dstValue.site = srcValue.site
			changed = true
		}
		dst[sym] = dstValue
	}
	return changed
}

func (a *analyzer) computeReferenceLiveness() {
	if a == nil || a.flow == nil || a.flow.graph == nil {
		return
	}
	a.referenceLiveIn = make(map[graph.NodeID]referenceLiveSet, len(a.flow.order))
	a.referenceLiveOut = make(map[graph.NodeID]referenceLiveSet, len(a.flow.order))
	queue := make([]graph.NodeID, 0, len(a.flow.order))
	queued := make(map[graph.NodeID]bool, len(a.flow.order))
	for i := len(a.flow.order) - 1; i >= 0; i-- {
		id := a.flow.order[i]
		queue = append(queue, id)
		queued[id] = true
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		queued[id] = false

		out := make(referenceLiveSet)
		for _, succ := range a.flow.graph.Successors(id) {
			mergeReferenceLiveSets(out, a.referenceLiveIn[succ])
		}
		uses, definitions := a.referenceUsesAndDefinitions(a.flow.nodes[id])
		in := cloneReferenceLiveSet(out)
		for sym := range definitions {
			delete(in, sym)
		}
		mergeReferenceLiveSets(in, uses)

		if sameReferenceLiveSet(a.referenceLiveIn[id], in) && sameReferenceLiveSet(a.referenceLiveOut[id], out) {
			continue
		}
		a.referenceLiveIn[id] = in
		a.referenceLiveOut[id] = out
		for _, pred := range a.flow.graph.Predecessors(id) {
			if !queued[pred] {
				queue = append(queue, pred)
				queued[pred] = true
			}
		}
	}
}

func (a *analyzer) referenceUsesAndDefinitions(node *flowNode) (referenceLiveSet, map[*symbols.Symbol]struct{}) {
	uses := make(referenceLiveSet)
	definitions := make(map[*symbols.Symbol]struct{})
	if a == nil || node == nil || node.kind != nodeStmt || node.stmt == nil {
		return uses, definitions
	}
	addDefinition := func(binding ast.Node) {
		if node.scope == nil || binding == nil {
			return
		}
		if sym, found := node.scope.LookupNode(binding); found {
			if _, reference := referenceMutability(sym); reference {
				definitions[sym] = struct{}{}
			}
		}
	}
	addUses := func(expr ast.Expr) {
		if expr == nil || a.module == nil || a.module.Semantics == nil {
			return
		}
		ast.Inspect(expr, func(current ast.Node) bool {
			ident, ok := current.(*ast.Ident)
			if !ok || ident == nil {
				return true
			}
			sym := a.module.Semantics.ResolvedSymbols[ident.ID()]
			if _, reference := referenceMutability(sym); reference {
				if previous, found := uses[sym]; !found {
					uses[sym] = ident
				} else {
					uses[sym] = earlierNode(previous, ident)
				}
			}
			return true
		})
	}

	switch stmt := node.stmt.(type) {
	case *ast.LetDecl:
		addDefinition(stmt)
		addUses(stmt.Value)
	case *ast.ConstDecl:
		addDefinition(stmt)
		addUses(stmt.Value)
	case *ast.AssignStmt:
		if target, ok := stmt.Target.(*ast.Ident); ok && node.scope != nil {
			if sym, found := node.scope.Lookup(target.Name); found {
				if _, reference := referenceMutability(sym); reference {
					definitions[sym] = struct{}{}
				}
			}
		} else {
			addUses(stmt.Target)
		}
		addUses(stmt.Value)
	case *ast.ReturnStmt:
		addUses(stmt.Value)
	case *ast.ExprStmt:
		addUses(stmt.Expr)
	case *ast.IfStmt:
		addUses(stmt.Cond)
	case *ast.ForStmt:
		addUses(stmt.Cond)
	}
	return uses, definitions
}

func cloneReferenceLiveSet(src referenceLiveSet) referenceLiveSet {
	dst := make(referenceLiveSet, len(src))
	for sym, site := range src {
		dst[sym] = site
	}
	return dst
}

func mergeReferenceLiveSets(dst, src referenceLiveSet) {
	for sym, site := range src {
		if previous, found := dst[sym]; !found {
			dst[sym] = site
		} else {
			dst[sym] = earlierNode(previous, site)
		}
	}
}

func sameReferenceLiveSet(left, right referenceLiveSet) bool {
	if len(left) != len(right) {
		return false
	}
	for sym, site := range left {
		if right[sym] != site {
			return false
		}
	}
	return true
}

func earlierNode(left, right ast.Node) ast.Node {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	leftLoc := ast.LocOf(left)
	rightLoc := ast.LocOf(right)
	if leftLoc == nil || leftLoc.Start == nil {
		return right
	}
	if rightLoc == nil || rightLoc.Start == nil || leftLoc.Start.Index <= rightLoc.Start.Index {
		return left
	}
	return right
}
