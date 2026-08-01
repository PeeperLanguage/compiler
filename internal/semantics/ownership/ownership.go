package ownership

import (
	"maps"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/project"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type site struct {
	flow  *cfg.Site
	stmt  ast.Stmt
	block *ast.BlockStmt
	scope *table.Scope
}

type analyzer struct {
	ctx              *project.CompilerContext
	module           *project.Module
	graph            *cfg.Graph
	sites            map[cfg.SiteID]*site
	order            []cfg.SiteID
	cleanup          *cfg.CleanupPlan
	function         *ast.FnDecl
	functionScope    *table.Scope
	reportedJoin     map[cfg.SiteID]bool
	inStates         map[cfg.SiteID]state
	referenceLiveIn  map[cfg.SiteID]map[*symbols.Symbol]ast.Node
	referenceLiveOut map[cfg.SiteID]map[*symbols.Symbol]ast.Node
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
	for _, graph := range module.CFG {
		if graph == nil {
			continue
		}
		graph.Cleanup = &cfg.CleanupPlan{
			AfterScope:     make(map[ir.NodeID][]symbols.SymbolID),
			BeforeReturn:   make(map[ir.NodeID][]symbols.SymbolID),
			BeforeAssign:   make(map[ir.NodeID]struct{}),
			DiscardedValue: make(map[ir.NodeID]struct{}),
			ProjectionBase: make(map[ir.NodeID]struct{}),
		}
	}
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
	if ctx == nil || module == nil || module.Semantics == nil || fn == nil || fn.Body == nil || scope == nil || cfgFn == nil || cfgFn.Cleanup == nil {
		return
	}
	sites, order := indexSites(module, cfgFn, fn.Body, scope)
	(&analyzer{
		ctx:           ctx,
		module:        module,
		graph:         cfgFn,
		sites:         sites,
		order:         order,
		cleanup:       cfgFn.Cleanup,
		function:      fn,
		functionScope: scope,
		reportedJoin:  make(map[cfg.SiteID]bool),
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

func indexSites(module *project.Module, cfgFn *cfg.Graph, body *ast.BlockStmt, scope *table.Scope) (map[cfg.SiteID]*site, []cfg.SiteID) {
	sites := make(map[cfg.SiteID]*site)
	order := make([]cfg.SiteID, 0)
	if module == nil || cfgFn == nil || body == nil || scope == nil {
		return sites, order
	}
	nodes := sourceNodes(module)
	scopes := sourceScopes(module, body, scope)
	for _, block := range cfgFn.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		currentScope := scope
		for _, flowSite := range block.Sites {
			if flowSite == nil {
				continue
			}
			indexed := &site{flow: flowSite, scope: currentScope}
			switch flowSite.Kind {
			case cfg.SiteStatement, cfg.SiteTerminator:
				if stmt, ok := nodes[hir.NodeID(flowSite.NodeID)].(ast.Stmt); ok && stmt != nil {
					indexed.stmt = stmt
					if resolved := scopes[hir.NodeID(flowSite.NodeID)]; resolved != nil {
						currentScope = resolved
						indexed.scope = resolved
					}
				}
			case cfg.SiteScopeExit:
				if blockStmt, ok := nodes[hir.NodeID(flowSite.NodeID)].(*ast.BlockStmt); ok && blockStmt != nil {
					indexed.block = blockStmt
					if resolved := scopes[hir.NodeID(flowSite.NodeID)]; resolved != nil {
						indexed.scope = resolved
					}
				}
			}
			sites[flowSite.ID] = indexed
			order = append(order, flowSite.ID)
		}
	}
	return sites, order
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
	if a == nil || a.graph == nil || a.graph.Entry == nil || len(a.graph.Entry.Sites) == 0 {
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
	entry := a.graph.Entry.Sites[0].ID
	a.inStates = map[cfg.SiteID]state{entry: entryState}
	queue := []cfg.SiteID{entry}
	queued := map[cfg.SiteID]bool{entry: true}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		queued[id] = false
		node := a.sites[id]
		next := copyState(a.inStates[id])
		if node != nil {
			switch node.flow.Kind {
			case cfg.SiteStatement, cfg.SiteTerminator:
				if node.stmt != nil {
					a.applyStmt(node, next)
				}
			case cfg.SiteScopeExit:
				a.applyBlockExit(node, next, a.newLoanContext(node, next))
			}
		}
		for _, succ := range node.flow.Successors {
			if a.sites[succ] == nil {
				continue
			}
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

func (a *analyzer) mergeState(nodeID cfg.SiteID, dst, src state, exists bool) (state, bool) {
	if !exists {
		return copyState(src), true
	}
	node := a.sites[nodeID]
	if node == nil || node.flow == nil || len(node.flow.Predecessors) <= 1 {
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

func (a *analyzer) applyBlockExit(node *site, st state, loans *loanContext) {
	if a == nil || node == nil || node.block == nil || node.scope == nil {
		return
	}
	a.checkScopeDestruction(node.scope, node.block, loans)
	delete(a.cleanup.AfterScope, ir.NodeID(node.block.ID()))
	cleanup := cleanupSymbols(node.scope, st)
	if len(cleanup) > 0 {
		a.cleanup.AfterScope[ir.NodeID(node.block.ID())] = symbolIDs(cleanup)
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
	delete(a.cleanup.BeforeReturn, ir.NodeID(stmt.ID()))
	cleanup := make([]*symbols.Symbol, 0)
	for current := scope; current != nil && current != a.module.ModuleScope; current = current.Parent() {
		a.checkScopeDestruction(current, stmt, loans)
		cleanup = append(cleanup, cleanupSymbols(current, st)...)
		clearScopeOwnership(current, st)
	}
	if len(cleanup) > 0 {
		a.cleanup.BeforeReturn[ir.NodeID(stmt.ID())] = symbolIDs(cleanup)
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

func (a *analyzer) applyStmt(node *site, st state) {
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
		delete(a.cleanup.BeforeAssign, ir.NodeID(s.ID()))
		a.checkExpr(scope, s.Value, st, useConsume, loans, false)
		if _, ok := s.Target.(*ast.Ident); !ok {
			a.checkExpr(scope, s.Target, st, useRead, loans, true)
			a.checkStorageAccess(scope, s.Target, st, loans, storageMutate)
			if typeinfo.NeedsDrop(a.exprType(s.Target)) {
				a.cleanup.BeforeAssign[ir.NodeID(s.ID())] = struct{}{}
			}
		}
		if target, ok := s.Target.(*ast.Ident); ok && scope != nil {
			if sym, found := scope.Lookup(target.Name); found {
				if _, referenceTarget := referenceMutability(sym); !referenceTarget {
					a.checkStorageAccess(scope, target, st, loans, storageMutate)
				}
				if typ, ok := symbols.GetSymbolType(sym); ok && typeinfo.NeedsDrop(typ) {
					if _, live := st.live[sym]; live {
						a.cleanup.BeforeAssign[ir.NodeID(s.ID())] = struct{}{}
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
			a.cleanup.DiscardedValue[ir.NodeID(s.Expr.ID())] = struct{}{}
		}
	case *ast.IfStmt:
		a.checkExpr(scope, s.Cond, st, useRead, loans, false)
	case *ast.ForStmt:
		a.checkExpr(scope, s.Cond, st, useRead, loans, false)
	}
}

func symbolIDs(values []*symbols.Symbol) []symbols.SymbolID {
	ids := make([]symbols.SymbolID, 0, len(values))
	for _, sym := range values {
		if sym != nil {
			ids = append(ids, sym.ID)
		}
	}
	return ids
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
