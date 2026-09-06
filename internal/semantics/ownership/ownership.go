package ownership

import (
	"maps"
	"slices"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	graphcore "compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/project"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

type site struct {
	cfgSite  *cfg.Site
	cfgBlock *cfg.Block
	stmt     ast.Stmt
	block    *ast.BlockStmt
	scope    *symbols.Scope
}

type analyzer struct {
	ctx                    *project.CompilerContext
	module                 *project.Module
	graph                  *cfg.Graph
	sites                  map[cfg.SiteID]*site
	order                  []cfg.SiteID
	effects                effect.SiteOps
	cleanup                *ownershipresult.CleanupPlan
	function               *ast.FnDecl
	functionScope          *symbols.Scope
	reportedJoin           map[cfg.SiteID]bool
	inStates               map[cfg.SiteID]state
	symbolLiveIn           map[cfg.SiteID]map[*symbols.Symbol]ast.Node
	symbolLiveOut          map[cfg.SiteID]map[*symbols.Symbol]ast.Node
	deadMatchCarrierAtExit map[cfg.SiteID]*symbols.Symbol
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
func Check(ctx *project.CompilerContext, module *project.Module) ownershipresult.Result {
	result := make(ownershipresult.Result)
	if ctx == nil || module == nil || module.AST == nil || module.ModuleScope == nil || module.Bindings == nil || module.Effects == nil || module.CFG == nil {
		return result
	}
	for _, graph := range module.CFG.Functions {
		if graph == nil {
			continue
		}
		result[graph.NodeID] = &ownershipresult.CleanupPlan{
			AfterScope:             make(map[cfg.SiteID][]symbols.SymbolID),
			BeforeReturn:           make(map[ir.NodeID][]symbols.SymbolID),
			BeforeAssign:           make(map[ir.NodeID]struct{}),
			DiscardedValue:         make(map[ir.NodeID]struct{}),
			ProjectionBase:         make(map[ir.NodeID]struct{}),
			MatchFieldDrops:        make(map[ir.NodeID][]int),
			MatchWholePayloadDrops: make(map[ir.NodeID]struct{}),
		}
	}
	for _, sym := range module.ModuleScope.Symbols() {
		if sym == nil || (sym.Kind != symbols.SymbolVar && sym.Kind != symbols.SymbolConst) {
			continue
		}
		if ownershipTrackedSymbol(sym) {
			ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
				"ownership-tracked module bindings are not supported", ast.LocOf(sym.ASTNode), "")
		}
	}
	for _, stmt := range module.AST.Stmts {
		switch node := stmt.(type) {
		case *ast.FnDecl:
			var sym *symbols.Symbol
			if node.Receiver != nil {
				sym = module.Bindings.MethodsByDecl[node.ID()]
			} else {
				sym, _ = module.ModuleScope.Lookup(node.Name.Name)
			}
			if sym == nil {
				continue
			}
			scope := sym.Scope
			graph := module.CFG.Function(ir.NodeID(node.ID()))
			if graph != nil {
				checkFunction(ctx, module, node, scope, graph, result[graph.NodeID])
			}
		}
	}
	return result
}

func checkFunction(ctx *project.CompilerContext, module *project.Module, fn *ast.FnDecl, scope *symbols.Scope, cfgFn *cfg.Graph, cleanup *ownershipresult.CleanupPlan) {
	if ctx == nil || module == nil || module.Bindings == nil || fn == nil || fn.Body == nil || scope == nil || cfgFn == nil || cleanup == nil {
		return
	}
	sites, order := indexSites(module, cfgFn, scope)
	(&analyzer{
		ctx:           ctx,
		module:        module,
		graph:         cfgFn,
		sites:         sites,
		order:         order,
		effects:       module.Effects[cfgFn.NodeID],
		cleanup:       cleanup,
		function:      fn,
		functionScope: scope,
		reportedJoin:  make(map[cfg.SiteID]bool),
	}).run()
}

func indexSites(module *project.Module, cfgFn *cfg.Graph, scope *symbols.Scope) (map[cfg.SiteID]*site, []cfg.SiteID) {
	sites := make(map[cfg.SiteID]*site)
	order := make([]cfg.SiteID, 0)
	if module == nil || module.Bindings == nil || cfgFn == nil || scope == nil {
		return sites, order
	}
	nodes := module.TypedASTNodes
	for _, block := range cfgFn.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		for _, flowSite := range block.Sites {
			if flowSite == nil {
				continue
			}
			resolvedScope := module.Bindings.BlockScopes[ast.NodeID(flowSite.ScopeID)]
			if resolvedScope == nil {
				resolvedScope = scope
			}
			indexed := &site{cfgSite: flowSite, cfgBlock: block, scope: resolvedScope}
			switch flowSite.Kind {
			case cfg.SiteStatement, cfg.SiteTerminator:
				if stmt, ok := nodes[ast.NodeID(flowSite.NodeID)].(ast.Stmt); ok && stmt != nil {
					indexed.stmt = stmt
				}
			case cfg.SiteScopeExit:
				if blockStmt, ok := nodes[ast.NodeID(flowSite.NodeID)].(*ast.BlockStmt); ok && blockStmt != nil {
					indexed.block = blockStmt
				}
			}
			sites[flowSite.ID] = indexed
			order = append(order, flowSite.ID)
		}
	}
	return sites, order
}

func (a *analyzer) run() {
	if a == nil || a.graph == nil || a.graph.Entry == nil || len(a.graph.Entry.Sites) == 0 {
		return
	}
	a.computeSymbolLiveness()
	a.planDeadMatchCarrierCleanup()
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
	work := graphcore.NewWorklist(entry)
	for {
		id, pending := work.Next()
		if !pending {
			break
		}
		node := a.sites[id]
		next := copyState(a.inStates[id])
		// Loop-owned loans are keyed by loop identity. Range loops publish no
		// Iterate effect, so releasing by loop ID is harmless and avoids asking
		// typechecker what kind of loop produced this CFG exit.
		if node != nil && node.cfgBlock != nil && node.cfgBlock.Origin == cfg.BlockLoopExit {
			releaseIterationLoans(next, nil, ast.NodeID(node.cfgBlock.NodeID))
		}
		if node != nil {
			switch node.cfgSite.Kind {
			case cfg.SiteStatement, cfg.SiteTerminator:
				if node.stmt != nil {
					a.applyStmt(node, next)
				}
			case cfg.SiteScopeExit:
				a.applyBlockExit(node, next, a.newLoanContext(node, next))
			}
		}
		for _, edge := range a.graph.SiteEdges.OutEdges(node.cfgSite.ID) {
			succ := edge.To
			if a.sites[succ] == nil {
				continue
			}
			edgeState := copyState(next)
			if edge.Kind == cfg.EdgeVariantCase {
				a.applyMatchEdge(node, edge, edgeState)
			}
			current, exists := a.inStates[succ]
			merged, changed := a.mergeState(succ, current, edgeState, exists)
			if !changed {
				continue
			}
			a.inStates[succ] = merged
			work.Add(succ)
		}
	}
}

func (a *analyzer) planDeadMatchCarrierCleanup() {
	a.deadMatchCarrierAtExit = make(map[cfg.SiteID]*symbols.Symbol)
	scopeExits := make(map[ast.NodeID][]*site)
	for _, node := range a.sites {
		if node != nil && node.cfgSite != nil && node.cfgSite.Kind == cfg.SiteScopeExit && node.block != nil {
			scopeExits[node.block.ID()] = append(scopeExits[node.block.ID()], node)
		}
	}

	for _, node := range a.sites {
		if node == nil || node.cfgSite == nil || node.cfgSite.Kind != cfg.SiteTerminator {
			continue
		}
		match, found := a.module.Typechecking.Matches[ast.NodeID(node.cfgSite.NodeID)]
		if !found {
			continue
		}
		_, carrier := a.matchSubjectCarrier(match)
		if carrier == nil {
			continue
		}

		exitsByJoin := make(map[cfg.SiteID][]*site)
		movesByJoin := make(map[cfg.SiteID]bool)
		armsByJoin := make(map[cfg.SiteID]map[ast.NodeID]struct{})
		for _, arm := range match.Arms {
			for _, exit := range scopeExits[arm.BodyID] {
				if exit == nil || exit.cfgSite == nil {
					continue
				}
				edges := a.graph.SiteEdges.OutEdges(exit.cfgSite.ID)
				if len(edges) != 1 {
					continue
				}
				join := edges[0].To
				for {
					joinNode := a.sites[join]
					if joinNode == nil || joinNode.cfgSite == nil {
						break
					}
					if joinNode.cfgSite.Kind != cfg.SiteScopeExit {
						exitsByJoin[join] = append(exitsByJoin[join], exit)
						if armsByJoin[join] == nil {
							armsByJoin[join] = make(map[ast.NodeID]struct{})
						}
						armsByJoin[join][arm.BodyID] = struct{}{}
						movesByJoin[join] = movesByJoin[join] || arm.CarrierUse == typeinfo.UseMove
						break
					}
					edges = a.graph.SiteEdges.OutEdges(joinNode.cfgSite.ID)
					if len(edges) != 1 {
						break
					}
					join = edges[0].To
				}
			}
		}

		for join, arms := range armsByJoin {
			if len(arms) < 2 || !movesByJoin[join] {
				continue
			}
			if _, live := a.symbolLiveIn[join][carrier]; live {
				continue
			}
			for _, exit := range exitsByJoin[join] {
				a.deadMatchCarrierAtExit[exit.cfgSite.ID] = carrier
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

// releaseIterationLoans ends synthetic carrier borrows when control leaves
// their loop. A zero loop ID releases all active loops, as required by return.
func releaseIterationLoans(st state, loans *loanContext, loopID ast.NodeID) {
	matches := func(loan referenceLoan) bool {
		return loan.loop != 0 && (loopID == 0 || loan.loop == loopID)
	}
	for holder, active := range st.references {
		remaining := slices.DeleteFunc(active, matches)
		if len(remaining) == 0 {
			delete(st.references, holder)
			continue
		}
		st.references[holder] = remaining
	}
	if loans != nil {
		loans.persistent = slices.DeleteFunc(loans.persistent, func(fact loanFact) bool {
			return matches(fact.loan)
		})
	}
}

func (a *analyzer) mergeState(nodeID cfg.SiteID, dst, src state, exists bool) (state, bool) {
	if !exists {
		return copyState(src), true
	}
	node := a.sites[nodeID]
	if node == nil || node.cfgSite == nil || a.graph.SiteEdges.InDegree(node.cfgSite.ID, nil) <= 1 {
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
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"ownership state differs across control-flow paths", ast.LocOf(node.stmt), "").
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
	if a == nil || node == nil || node.cfgSite == nil || node.block == nil || node.scope == nil {
		return
	}
	a.checkScopeDestruction(node.scope, node.block, loans)
	delete(a.cleanup.AfterScope, node.cfgSite.ID)
	cleanup := cleanupSymbols(node.scope, st)
	if carrier := a.deadMatchCarrierAtExit[node.cfgSite.ID]; carrier != nil {
		if _, live := st.live[carrier]; live {
			a.reportLoanConflict([]place.Origin{{Root: carrier}}, nil, storageDestroy, node.block, loans)
			if typ, ok := symbols.GetSymbolType(carrier); ok && typeinfo.OwnershipCapabilityOf(typ).Drop {
				cleanup = append(cleanup, carrier)
			}
			st.moved[carrier] = node.block
			delete(st.live, carrier)
			delete(st.references, carrier)
		}
	}
	if len(cleanup) > 0 {
		a.cleanup.AfterScope[node.cfgSite.ID] = symbolIDs(cleanup)
	}
	clearScopeOwnership(node.scope, st)
}

func clearScopeOwnership(scope *symbols.Scope, st state) {
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

func cleanupSymbols(scope *symbols.Scope, st state) []*symbols.Symbol {
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
		if ok && typeinfo.OwnershipCapabilityOf(typ).Drop {
			cleanup = append(cleanup, sym)
		}
	}
	return cleanup
}

func (a *analyzer) cleanupBeforeReturn(scope *symbols.Scope, stmt *ast.ReturnStmt, st state, loans *loanContext) {
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

func (a *analyzer) checkScopeDestruction(scope *symbols.Scope, site ast.Node, loans *loanContext) {
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

// planDiscardedDrops records a drop for each value the site produces and throws
// away.
//
// Only a temporary is discarded in the sense that matters: a value that names
// no binding has nobody left to own it, so the drop happens here. A discarded
// expression that names storage still belongs to that storage. The producer
// makes the distinction, so this no longer asks the syntax.
func (a *analyzer) planDiscardedDrops(node *site) {
	if a == nil || a.cleanup == nil || node == nil || node.cfgSite == nil {
		return
	}
	for _, op := range a.effects[node.cfgSite.ID] {
		discard, isDiscard := op.(effect.Discard)
		if !isDiscard || discard.Place.Root != nil {
			continue
		}
		if typeinfo.OwnershipCapabilityOf(a.module.EffectiveExprType(discard.Node)).Drop {
			a.cleanup.DiscardedValue[ir.NodeID(discard.Node)] = struct{}{}
		}
	}
}

func (a *analyzer) applyStmt(node *site, st state) {
	if a == nil || node == nil || node.scope == nil || node.stmt == nil {
		return
	}
	scope := node.scope
	loans := a.newLoanContext(node, st)

	// Return provenance has to be checked against the incoming state, before
	// evaluating the returned value can move its source. Storage transitions for
	// declarations and assignments are published effects and require no syntax
	// cases here.
	if s, ok := node.stmt.(*ast.ReturnStmt); ok {
		a.checkPointerEscape(scope, s.Value, st)
		a.validateReferenceReturn(scope, s, st)
	}

	// Evaluation and generic storage transitions come from published effects.
	a.applyEffects(node, st, loans)
	a.planDiscardedDrops(node)

	// Return remains the one ownership statement policy whose checks straddle
	// evaluation: provenance is validated above before the value can move, while
	// cleanup happens after its effects have executed.
	if s, ok := node.stmt.(*ast.ReturnStmt); ok {
		releaseIterationLoans(st, loans, 0)
		a.cleanupBeforeReturn(scope, s, st, loans)
	}
}

func (a *analyzer) applyMatchEdge(node *site, edge cfg.Edge, st state) {
	if a == nil || node == nil || node.cfgSite == nil || edge.Kind != cfg.EdgeVariantCase {
		return
	}
	match, found := a.module.Typechecking.Matches[ast.NodeID(node.cfgSite.NodeID)]
	if !found {
		return
	}
	arm, found := match.Arm(edge.Case)
	if !found || arm.Payload == nil {
		return
	}
	subject, carrier := a.matchSubjectCarrier(match)
	if subject == nil {
		return
	}
	movesCarrier := arm.CarrierUse == typeinfo.UseMove
	listed := make(map[int]bool, len(arm.Bindings))
	for _, field := range arm.Bindings {
		switch field.Projection {
		case typecheckresult.MatchPayloadField:
			listed[field.Field] = field.Discard
		case typecheckresult.MatchWholePayload:
		default:
			panic("ownership: invalid match binding projection")
		}
	}
	if movesCarrier && carrier == nil {
		a.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"move-only variant payload cannot be moved from partial place; borrow it instead", ast.LocOf(subject), "")
	}
	if movesCarrier && carrier != nil {
		moveSite, _ := a.module.TypedASTNodes[arm.ArmID]
		if moveSite == nil {
			moveSite = subject
		}
		a.reportLoanConflict(a.originsForExpr(subject), nil, storageConsume, moveSite, a.newLoanContext(node, st))
		st.moved[carrier] = moveSite
		delete(st.live, carrier)
		delete(st.references, carrier)
		if len(arm.Bindings) == 1 && arm.Bindings[0].Projection == typecheckresult.MatchWholePayload {
			if arm.Bindings[0].Discard && typeinfo.OwnershipCapabilityOf(arm.Bindings[0].Type).Drop {
				a.cleanup.MatchWholePayloadDrops[ir.NodeID(arm.BodyID)] = struct{}{}
			}
		} else if payload, payloadFound := typeinfo.Underlying(arm.Payload).(*typeinfo.StructType); payloadFound && payload != nil {
			drops := make([]int, 0)
			for fieldIndex := len(payload.Fields) - 1; fieldIndex >= 0; fieldIndex-- {
				field := payload.Fields[fieldIndex]
				discarded, selected := listed[fieldIndex]
				if typeinfo.OwnershipCapabilityOf(field.Type).Drop && (!selected || discarded) {
					drops = append(drops, fieldIndex)
				}
			}
			if len(drops) > 0 {
				a.cleanup.MatchFieldDrops[ir.NodeID(arm.BodyID)] = drops
			}
		}
	}
	for _, field := range arm.Bindings {
		binding := field.Binding
		if binding == nil {
			continue
		}
		if ownershipTrackedSymbol(binding) {
			delete(st.moved, binding)
			st.live[binding] = struct{}{}
		}
		if binding.ASTNode == nil || a.module.Flow == nil {
			continue
		}
		origins := place.CloneOrigins(a.module.Flow.ResolvedValueOrigins[binding.ASTNode.ID()])
		if mutable, reference := referenceMutability(binding); reference && len(origins) > 0 {
			st.references[binding] = []referenceLoan{{
				id: loanID{node: binding.ASTNode}, origins: origins, mutable: mutable, site: binding.ASTNode,
			}}
		}
	}
}

func (a *analyzer) matchSubjectCarrier(match typecheckresult.Match) (ast.Expr, *symbols.Symbol) {
	subject, _ := a.module.TypedASTNodes[match.SubjectID].(ast.Expr)
	ident, direct := subject.(*ast.Ident)
	if !direct {
		return subject, nil
	}
	carrier := a.module.Bindings.NodeSymbols[ident.ID()]
	if carrier == nil || (carrier.Kind != symbols.SymbolVar && carrier.Kind != symbols.SymbolConst && carrier.Kind != symbols.SymbolParam) {
		return subject, nil
	}
	return subject, carrier
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
