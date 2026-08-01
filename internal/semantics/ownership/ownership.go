package ownership

import (
	"maps"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir/hir"
	"compiler/internal/project"
	"compiler/internal/semantics/cfg"
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

type flowNodeID uint32

type flowNode struct {
	id    flowNodeID
	kind  nodeKind
	stmt  ast.Stmt
	block *ast.BlockStmt
	scope *table.Scope
}

type flow struct {
	nodes        map[flowNodeID]*flowNode
	successors   map[flowNodeID][]flowNodeID
	predecessors map[flowNodeID][]flowNodeID
	order        []flowNodeID
	next         flowNodeID
	entry        flowNodeID
	exit         flowNodeID
}

type analyzer struct {
	ctx              *project.CompilerContext
	module           *project.Module
	flow             *flow
	function         *ast.FnDecl
	functionScope    *table.Scope
	reportedJoin     map[flowNodeID]bool
	inStates         map[flowNodeID]state
	referenceLiveIn  map[flowNodeID]map[*symbols.Symbol]ast.Node
	referenceLiveOut map[flowNodeID]map[*symbols.Symbol]ast.Node
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
	clear(module.Semantics.DropDiscardedExpr)
	clear(module.Semantics.DropProjectionBase)
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
			checkFunction(ctx, module, node, scope, cfgForFunction(module, node))
		}
	}
}

func checkFunction(ctx *project.CompilerContext, module *project.Module, fn *ast.FnDecl, scope *table.Scope, cfgFn *cfg.Graph) {
	if ctx == nil || module == nil || module.Semantics == nil || fn == nil || fn.Body == nil || scope == nil || cfgFn == nil {
		return
	}
	f := build(module, cfgFn, fn.Body, scope)
	(&analyzer{
		ctx:           ctx,
		module:        module,
		flow:          f,
		function:      fn,
		functionScope: scope,
		reportedJoin:  make(map[flowNodeID]bool),
	}).run()
}

func cfgForFunction(module *project.Module, fn *ast.FnDecl) *cfg.Graph {
	if module == nil || fn == nil {
		return nil
	}
	for _, graph := range module.CFG {
		if graph != nil && graph.Source != nil && graph.Source.NodeID == hir.NodeID(fn.ID()) {
			return graph
		}
	}
	return nil
}

type flowEndpoints struct {
	first flowNodeID
	last  flowNodeID
}

func build(module *project.Module, cfgFn *cfg.Graph, body *ast.BlockStmt, scope *table.Scope) *flow {
	if module == nil || cfgFn == nil || body == nil || scope == nil {
		return nil
	}
	f := &flow{
		nodes:        make(map[flowNodeID]*flowNode),
		successors:   make(map[flowNodeID][]flowNodeID),
		predecessors: make(map[flowNodeID][]flowNodeID),
		order:        make([]flowNodeID, 0),
	}
	entry := newFlowNode(f, nodeEntry, nil, scope)
	exit := newFlowNode(f, nodeExit, nil, scope)
	f.entry = entry.id
	f.exit = exit.id
	nodes := sourceNodes(module)
	scopes := sourceScopes(module, body, scope)
	ends := make(map[*cfg.Block]flowEndpoints, len(cfgFn.Blocks))
	for _, block := range cfgFn.Blocks {
		if block == nil || !block.Reachable || block == cfgFn.Exit {
			continue
		}
		currentScope := scope
		var first, last flowNodeID
		appendSite := func(kind nodeKind, stmt ast.Stmt, blockStmt *ast.BlockStmt, siteScope *table.Scope) {
			if siteScope == nil {
				siteScope = currentScope
			}
			node := newFlowNode(f, kind, stmt, siteScope)
			node.block = blockStmt
			if first == 0 {
				first = node.id
			} else {
				connect(f, last, node.id)
			}
			last = node.id
		}
		for _, stmt := range block.Stmts {
			siteID := hir.NodeIDOf(stmt)
			astStmt, ok := nodes[siteID].(ast.Stmt)
			if !ok || astStmt == nil {
				continue
			}
			currentScope = scopes[siteID]
			appendSite(nodeStmt, astStmt, nil, currentScope)
		}
		for _, scopeID := range block.ScopeExits {
			blockStmt, ok := nodes[scopeID].(*ast.BlockStmt)
			if !ok || blockStmt == nil {
				continue
			}
			appendSite(nodeBlockExit, nil, blockStmt, scopes[scopeID])
		}
		if branch, ok := block.Terminator.(*cfg.Branch); ok {
			if stmt, ok := nodes[branch.NodeID].(ast.Stmt); ok && stmt != nil {
				appendSite(nodeStmt, stmt, nil, scopes[branch.NodeID])
			}
		}
		if first == 0 {
			appendSite(nodeJoin, nil, nil, currentScope)
		}
		ends[block] = flowEndpoints{first: first, last: last}
	}
	entryBlock := ends[cfgFn.Entry]
	if entryBlock.first == 0 {
		return f
	}
	connect(f, entry.id, entryBlock.first)
	for block, endpoints := range ends {
		switch term := block.Terminator.(type) {
		case *cfg.Jump:
			if term.Target == cfgFn.Exit {
				connect(f, endpoints.last, exit.id)
			} else if target := ends[term.Target]; target.first != 0 {
				connect(f, endpoints.last, target.first)
			}
		case *cfg.Branch:
			if target := ends[term.TrueTarget]; target.first != 0 {
				connect(f, endpoints.last, target.first)
			}
			if target := ends[term.FalseTarget]; target.first != 0 {
				connect(f, endpoints.last, target.first)
			}
		case *cfg.Return:
			connect(f, endpoints.last, exit.id)
		}
	}
	return f
}

func newFlowNode(f *flow, kind nodeKind, stmt ast.Stmt, scope *table.Scope) *flowNode {
	f.next++
	id := f.next
	node := &flowNode{id: id, kind: kind, stmt: stmt, scope: scope}
	f.nodes[id] = node
	f.order = append(f.order, id)
	return node
}

func connect(f *flow, from, to flowNodeID) {
	if f == nil || from == 0 || to == 0 {
		return
	}
	for _, existing := range f.successors[from] {
		if existing == to {
			return
		}
	}
	f.successors[from] = append(f.successors[from], to)
	f.predecessors[to] = append(f.predecessors[to], from)
}

func sourceNodes(module *project.Module) map[hir.NodeID]ast.Node {
	nodes := make(map[hir.NodeID]ast.Node)
	if module == nil || module.AST == nil {
		return nodes
	}
	for _, stmt := range module.AST.Stmts {
		ast.Inspect(stmt, func(node ast.Node) bool {
			if node != nil {
				nodes[hir.NodeID(node.ID())] = node
			}
			return true
		})
	}
	return nodes
}

func sourceScopes(module *project.Module, body *ast.BlockStmt, root *table.Scope) map[hir.NodeID]*table.Scope {
	indexed := make(map[hir.NodeID]*table.Scope)
	if module == nil || module.Semantics == nil || body == nil || root == nil {
		return indexed
	}
	var indexStmt func(ast.Stmt, *table.Scope)
	var indexBlock func(*ast.BlockStmt, *table.Scope)
	indexBlock = func(block *ast.BlockStmt, parent *table.Scope) {
		if block == nil {
			return
		}
		scope := parent
		if resolved := module.Semantics.BlockScopes[block.ID()]; resolved != nil {
			scope = resolved
		}
		indexed[hir.NodeID(block.ID())] = scope
		for _, stmt := range block.Stmts {
			indexStmt(stmt, scope)
		}
	}
	indexStmt = func(stmt ast.Stmt, scope *table.Scope) {
		if stmt == nil {
			return
		}
		indexed[hir.NodeID(stmt.ID())] = scope
		switch node := stmt.(type) {
		case *ast.BlockStmt:
			indexBlock(node, scope)
		case *ast.IfStmt:
			indexBlock(node.Then, scope)
			indexStmt(node.Else, scope)
		case *ast.ForStmt:
			indexBlock(node.Body, scope)
		}
	}
	indexBlock(body, root)
	return indexed
}

func (a *analyzer) run() {
	if a == nil || a.flow == nil || a.flow.entry == 0 {
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
	a.inStates = map[flowNodeID]state{a.flow.entry: entryState}
	queue := []flowNodeID{a.flow.entry}
	queued := map[flowNodeID]bool{a.flow.entry: true}
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
		for _, succ := range a.flow.successors[id] {
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

func (a *analyzer) mergeState(nodeID flowNodeID, dst, src state, exists bool) (state, bool) {
	if !exists {
		return copyState(src), true
	}
	if len(a.flow.predecessors[nodeID]) <= 1 {
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
