package ownership

import (
	"fmt"
	"maps"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/graph"
	"compiler/internal/project"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type nodeKind uint8

const (
	nodeEntry nodeKind = iota
	nodeStmt
	nodeJoin
	nodeBlockExit
	nodeExit
)

const (
	graphNodeFlow graph.NodeKind = "ownership_flow"
	graphEdgeFlow graph.EdgeKind = "ownership_flow"
)

type flowNode struct {
	id    graph.NodeID
	kind  nodeKind
	stmt  ast.Stmt
	block *ast.BlockStmt
	scope *table.Scope
}

type flow struct {
	graph *graph.Graph
	nodes map[graph.NodeID]*flowNode
	order []graph.NodeID
	next  int
	entry graph.NodeID
	exit  graph.NodeID
}

type builder struct {
	module *project.Module
	flow   *flow
}

type analyzer struct {
	ctx              *project.CompilerContext
	module           *project.Module
	flow             *flow
	function         *ast.FnDecl
	functionScope    *table.Scope
	reportedJoin     map[graph.NodeID]bool
	inStates         map[graph.NodeID]state
	referenceLiveIn  map[graph.NodeID]map[*symbols.Symbol]ast.Node
	referenceLiveOut map[graph.NodeID]map[*symbols.Symbol]ast.Node
}

type pointerOrigin struct {
	root *symbols.Symbol
	site ast.Node
}

type state struct {
	moved      map[*symbols.Symbol]ast.Node
	live       map[*symbols.Symbol]struct{}
	pointers   map[*symbols.Symbol]pointerOrigin
	references map[*symbols.Symbol][]referenceLoan
}

// Check runs flow-sensitive ownership checks after typechecking has populated
// expression types and scopes. Keeping this phase outside the checker prevents
// value-flow rules from becoming ad hoc type rules.
func Check(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || module == nil || module.AST == nil || module.ModuleScope == nil || module.Semantics == nil {
		return
	}
	clear(module.Semantics.CleanupAfterBlock)
	clear(module.Semantics.CleanupBeforeReturn)
	clear(module.Semantics.DropBeforeAssign)
	for _, stmt := range module.AST.Stmts {
		switch node := stmt.(type) {
		case *ast.LetDecl, *ast.ConstDecl:
			sym, found := module.ModuleScope.LookupNode(node)
			if !found || sym == nil {
				continue
			}
			if ownershipTrackedSymbol(sym) {
				ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
					"ownership-tracked module bindings are not supported", ast.LocOf(node), "")
			}
		case *ast.FnDecl:
			var sym *symbols.Symbol
			if node.Receiver != nil {
				sym = module.Semantics.MethodSymbol[node.ID()]
			} else {
				sym, _ = module.ModuleScope.Lookup(node.Name.Name)
			}
			if sym == nil {
				continue
			}
			scope, _ := sym.Scope.(*table.Scope)
			checkFunction(ctx, module, node, scope)
		}
	}
}

func checkFunction(ctx *project.CompilerContext, module *project.Module, fn *ast.FnDecl, scope *table.Scope) {
	if ctx == nil || module == nil || module.Semantics == nil || fn == nil || fn.Body == nil || scope == nil {
		return
	}
	f := build(module, fn.Body, scope)
	(&analyzer{
		ctx:           ctx,
		module:        module,
		flow:          f,
		function:      fn,
		functionScope: scope,
		reportedJoin:  make(map[graph.NodeID]bool),
	}).run()
}

func build(module *project.Module, body *ast.BlockStmt, scope *table.Scope) *flow {
	f := &flow{
		graph: graph.New(graphNodeFlow, graphEdgeFlow),
		nodes: make(map[graph.NodeID]*flowNode),
		order: make([]graph.NodeID, 0),
	}
	b := &builder{module: module, flow: f}
	entry := b.newNode(nodeEntry, nil, scope)
	exit := b.newNode(nodeExit, nil, scope)
	f.entry = entry.id
	f.exit = exit.id
	tails := b.buildBlock([]graph.NodeID{entry.id}, body, scope)
	b.connectAll(tails, exit.id)
	return f
}

func (b *builder) newNode(kind nodeKind, stmt ast.Stmt, scope *table.Scope) *flowNode {
	id := graph.NodeID(fmt.Sprintf("ownership:%d", b.flow.next))
	b.flow.next++
	node := &flowNode{id: id, kind: kind, stmt: stmt, scope: scope}
	b.flow.graph.AddNode(id)
	b.flow.nodes[id] = node
	b.flow.order = append(b.flow.order, id)
	return node
}

func (b *builder) connect(from, to graph.NodeID) {
	if from == "" || to == "" {
		return
	}
	b.flow.graph.AddEdge(from, to)
}

func (b *builder) connectAll(from []graph.NodeID, to graph.NodeID) {
	for _, id := range from {
		b.connect(id, to)
	}
}

func (b *builder) blockScope(block *ast.BlockStmt, fallback *table.Scope) *table.Scope {
	if b == nil || b.module == nil || b.module.Semantics == nil || block == nil {
		return fallback
	}
	if scope, ok := b.module.Semantics.BlockScopes[block.ID()]; ok && scope != nil {
		return scope
	}
	return fallback
}

func (b *builder) buildBlock(in []graph.NodeID, block *ast.BlockStmt, fallback *table.Scope) []graph.NodeID {
	if block == nil {
		return in
	}
	scope := b.blockScope(block, fallback)
	tails := in
	for _, stmt := range block.Stmts {
		tails = b.buildStmt(tails, stmt, scope)
		if len(tails) == 0 {
			break
		}
	}
	if len(tails) == 0 {
		return nil
	}
	exit := b.newNode(nodeBlockExit, nil, scope)
	exit.block = block
	b.connectAll(tails, exit.id)
	return []graph.NodeID{exit.id}
}

func (b *builder) buildStmt(in []graph.NodeID, stmt ast.Stmt, scope *table.Scope) []graph.NodeID {
	if stmt == nil {
		return in
	}
	switch s := stmt.(type) {
	case *ast.BlockStmt:
		return b.buildBlock(in, s, scope)
	case *ast.IfStmt:
		node := b.newNode(nodeStmt, stmt, scope)
		b.connectAll(in, node.id)
		join := b.newNode(nodeJoin, stmt, scope)
		thenTails := b.buildBlock([]graph.NodeID{node.id}, s.Then, scope)
		b.connectAll(thenTails, join.id)
		if s.Else != nil {
			elseTails := b.buildStmt([]graph.NodeID{node.id}, s.Else, scope)
			b.connectAll(elseTails, join.id)
		} else {
			b.connect(node.id, join.id)
		}
		return []graph.NodeID{join.id}
	case *ast.ForStmt:
		header := b.newNode(nodeStmt, stmt, scope)
		b.connectAll(in, header.id)
		after := b.newNode(nodeJoin, stmt, scope)
		bodyTails := b.buildBlock([]graph.NodeID{header.id}, s.Body, scope)
		b.connectAll(bodyTails, header.id)
		b.connect(header.id, after.id)
		return []graph.NodeID{after.id}
	case *ast.ReturnStmt:
		node := b.newNode(nodeStmt, stmt, scope)
		b.connectAll(in, node.id)
		b.connect(node.id, b.flow.exit)
		return nil
	default:
		node := b.newNode(nodeStmt, stmt, scope)
		b.connectAll(in, node.id)
		return []graph.NodeID{node.id}
	}
}

func (a *analyzer) run() {
	if a == nil || a.flow == nil || a.flow.graph == nil || a.flow.entry == "" {
		return
	}
	a.computeReferenceLiveness()
	entryState := newState()
	for _, sym := range a.functionScope.Symbols() {
		if sym == nil || sym.Kind != symbols.SymbolParam {
			continue
		}
		if ownershipTrackedSymbol(sym) {
			entryState.live[sym] = struct{}{}
		}
		if mutable, reference := referenceMutability(sym); reference {
			entryState.references[sym] = []referenceLoan{{
				id:      loanID{parameter: sym},
				origins: []place.Origin{{Root: sym}},
				mutable: mutable,
				site:    sym.ASTNode,
			}}
		}
	}
	a.inStates = map[graph.NodeID]state{a.flow.entry: entryState}
	queue := []graph.NodeID{a.flow.entry}
	queued := map[graph.NodeID]bool{a.flow.entry: true}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		queued[id] = false
		node := a.flow.nodes[id]
		next := copyState(a.inStates[id])
		if node != nil {
			switch node.kind {
			case nodeStmt:
				if node.stmt != nil {
					a.applyStmt(node, next)
				}
			case nodeBlockExit:
				a.applyBlockExit(node, next, a.newLoanContext(node, next))
			}
		}
		for _, succ := range a.flow.graph.Successors(id) {
			current, exists := a.inStates[succ]
			merged, changed := a.mergeState(succ, current, next, exists)
			if !changed {
				continue
			}
			a.inStates[succ] = merged
			if !queued[succ] {
				queue = append(queue, succ)
				queued[succ] = true
			}
		}
	}
}

func copyState(src state) state {
	dst := newState()
	maps.Copy(dst.moved, src.moved)
	maps.Copy(dst.live, src.live)
	maps.Copy(dst.pointers, src.pointers)
	for sym, value := range src.references {
		dst.references[sym] = copyReferenceLoans(value)
	}
	return dst
}

func (a *analyzer) mergeState(nodeID graph.NodeID, dst, src state, exists bool) (state, bool) {
	if !exists {
		return copyState(src), true
	}
	if a.flow.graph.InDegree(nodeID) <= 1 {
		if maps.Equal(dst.moved, src.moved) && maps.Equal(dst.live, src.live) && maps.Equal(dst.pointers, src.pointers) &&
			sameReferenceValues(dst.references, src.references) {
			return dst, false
		}
		return copyState(src), true
	}
	changed := false
	mismatch := false
	for sym := range dst.live {
		if _, ok := src.live[sym]; ok {
			continue
		}
		delete(dst.live, sym)
		changed = true
		mismatch = true
	}
	for sym := range src.live {
		if _, ok := dst.live[sym]; !ok {
			mismatch = true
		}
	}
	if mismatch && !a.reportedJoin[nodeID] {
		a.reportedJoin[nodeID] = true
		node := a.flow.nodes[nodeID]
		var site ast.Node
		if node != nil {
			site = node.stmt
		}
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"ownership state differs across control-flow paths", ast.LocOf(site), "").
			WithHelp("move or reinitialize ownership-tracked values on every path")
	}
	for sym, site := range src.moved {
		if _, ok := dst.moved[sym]; ok {
			continue
		}
		dst.moved[sym] = site
		changed = true
	}
	for sym, origin := range src.pointers {
		if _, ok := dst.pointers[sym]; ok {
			continue
		}
		dst.pointers[sym] = origin
		changed = true
	}
	if mergeReferenceValues(dst.references, src.references) {
		changed = true
	}
	return dst, changed
}

func newState() state {
	return state{
		moved:      make(map[*symbols.Symbol]ast.Node),
		live:       make(map[*symbols.Symbol]struct{}),
		pointers:   make(map[*symbols.Symbol]pointerOrigin),
		references: make(map[*symbols.Symbol][]referenceLoan),
	}
}

func (a *analyzer) applyBlockExit(node *flowNode, st state, loans *loanContext) {
	if a == nil || node == nil || node.block == nil || node.scope == nil {
		return
	}
	a.checkScopeDestruction(node.scope, node.block, loans)
	delete(a.module.Semantics.CleanupAfterBlock, node.block.ID())
	cleanup := cleanupSymbols(node.scope, st)
	if len(cleanup) > 0 {
		a.module.Semantics.CleanupAfterBlock[node.block.ID()] = cleanup
	}
	clearScopeOwnership(node.scope, st)
}

func clearScopeOwnership(scope *table.Scope, st state) {
	if scope == nil {
		return
	}
	for _, sym := range scope.Symbols() {
		delete(st.live, sym)
		delete(st.moved, sym)
		delete(st.pointers, sym)
		delete(st.references, sym)
	}
}

func cleanupSymbols(scope *table.Scope, st state) []*symbols.Symbol {
	if scope == nil {
		return nil
	}
	symbolsInScope := scope.Symbols()
	cleanup := make([]*symbols.Symbol, 0)
	for _, sym := range slices.Backward(symbolsInScope) {
		if sym == nil {
			continue
		}
		if _, live := st.live[sym]; !live {
			continue
		}
		typ, ok := symbols.GetSymbolType(sym)
		if ok && typeinfo.NeedsDrop(typ) {
			cleanup = append(cleanup, sym)
		}
	}
	return cleanup
}

func (a *analyzer) cleanupBeforeReturn(scope *table.Scope, stmt *ast.ReturnStmt, st state, loans *loanContext) {
	if a == nil || stmt == nil {
		return
	}
	delete(a.module.Semantics.CleanupBeforeReturn, stmt.ID())
	cleanup := make([]*symbols.Symbol, 0)
	for current := scope; current != nil && current != a.module.ModuleScope; current = current.Parent() {
		a.checkScopeDestruction(current, stmt, loans)
		cleanup = append(cleanup, cleanupSymbols(current, st)...)
		clearScopeOwnership(current, st)
	}
	if len(cleanup) > 0 {
		a.module.Semantics.CleanupBeforeReturn[stmt.ID()] = cleanup
	}
}

func (a *analyzer) checkScopeDestruction(scope *table.Scope, site ast.Node, loans *loanContext) {
	if a == nil || scope == nil || loans == nil {
		return
	}
	for _, sym := range scope.Symbols() {
		if sym == nil || (sym.Kind != symbols.SymbolVar && sym.Kind != symbols.SymbolConst && sym.Kind != symbols.SymbolParam) {
			continue
		}
		if _, reference := referenceMutability(sym); reference {
			continue
		}
		a.reportLoanConflict([]place.Origin{{Root: sym}}, nil, storageDestroy, site, loans)
	}
}

func (a *analyzer) applyStmt(node *flowNode, st state) {
	if a == nil || node == nil || node.scope == nil || node.stmt == nil {
		return
	}
	scope := node.scope
	loans := a.newLoanContext(node, st)
	switch s := node.stmt.(type) {
	case *ast.LetDecl:
		a.applyBinding(scope, s, s.Value, st, loans)
	case *ast.ConstDecl:
		a.applyBinding(scope, s, s.Value, st, loans)
	case *ast.AssignStmt:
		reference, hasReference := a.referenceValueForExpr(scope, s.Value, st)
		delete(a.module.Semantics.DropBeforeAssign, s.ID())
		a.checkExpr(scope, s.Value, st, useConsume, loans, false)
		if _, ok := s.Target.(*ast.Ident); !ok {
			a.checkExpr(scope, s.Target, st, useRead, loans, true)
			a.checkStorageAccess(scope, s.Target, st, loans, storageMutate)
			if typeinfo.NeedsDrop(a.exprType(s.Target)) {
				a.module.Semantics.DropBeforeAssign[s.ID()] = struct{}{}
			}
		}
		if target, ok := s.Target.(*ast.Ident); ok && scope != nil {
			if sym, found := scope.Lookup(target.Name); found {
				if _, referenceTarget := referenceMutability(sym); !referenceTarget {
					a.checkStorageAccess(scope, target, st, loans, storageMutate)
				}
				if typ, ok := symbols.GetSymbolType(sym); ok && typeinfo.NeedsDrop(typ) {
					if _, live := st.live[sym]; live {
						a.module.Semantics.DropBeforeAssign[s.ID()] = struct{}{}
					}
				}
				if ownershipTrackedSymbol(sym) {
					delete(st.moved, sym)
					st.live[sym] = struct{}{}
				}
				a.updatePointerSymbol(sym, scope, s.Value, st)
				a.updateReferenceSymbol(sym, reference, hasReference, st)
			}
		}
	case *ast.ReturnStmt:
		a.checkPointerEscape(scope, s.Value, st)
		a.validateReferenceReturn(scope, s, st)
		a.checkExpr(scope, s.Value, st, useConsume, loans, false)
		a.cleanupBeforeReturn(scope, s, st, loans)
	case *ast.ExprStmt:
		a.checkExpr(scope, s.Expr, st, useRead, loans, false)
		if s.Expr != nil && !place.IsPlaceExpr(s.Expr) && typeinfo.NeedsDrop(a.exprType(s.Expr)) {
			a.module.Semantics.DropDiscardedExpr[s.Expr.ID()] = struct{}{}
		}
	case *ast.IfStmt:
		a.checkExpr(scope, s.Cond, st, useRead, loans, false)
	case *ast.ForStmt:
		a.checkExpr(scope, s.Cond, st, useRead, loans, false)
	}
}

func (a *analyzer) applyBinding(scope *table.Scope, stmt ast.Stmt, value ast.Expr, st state, loans *loanContext) {
	if scope == nil || stmt == nil {
		return
	}
	reference, hasReference := a.referenceValueForExpr(scope, value, st)
	a.checkExpr(scope, value, st, useConsume, loans, false)
	sym, found := scope.LookupNode(stmt)
	if !found || sym == nil {
		return
	}
	a.updatePointerSymbol(sym, scope, value, st)
	a.updateReferenceSymbol(sym, reference, hasReference, st)
	if ownershipTrackedSymbol(sym) {
		delete(st.moved, sym)
		st.live[sym] = struct{}{}
	}
}
