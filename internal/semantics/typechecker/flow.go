package typechecker

import (
	"maps"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/project"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type variantStateFact struct {
	origins      []place.Origin
	cases        []int
	caseCount    int
	dependencies []*symbols.Symbol
}

type flowState struct {
	reachable   bool
	variants    []variantStateFact
	references  map[*symbols.Symbol][]place.Origin
	rawPointers map[*symbols.Symbol][]place.Origin
}

type flowCheck struct {
	result   *flowresult.Result
	state    *flowState
	analyzer *flowAnalyzer
	events   *flowExpressionEvents
}

type flowCallEvent struct {
	order int
	call  *ast.CallExpr
}

type flowExpressionEvents struct {
	next  int
	tests map[ast.NodeID]int
	calls []flowCallEvent
}

type edgeVariantFact struct {
	variant variantStateFact
	order   int
}

type flowAnalyzer struct {
	ctx           *project.CompilerContext
	module        *project.Module
	functionScope *symbols.Scope
	graph         *cfg.Graph
	returnType    typeinfo.Type
	result        *flowresult.Result
	sites         map[cfg.SiteID]*cfg.Site
	inStates      map[cfg.SiteID]flowState
}

// CheckFlow runs optional/origin facts to fixed point, then records exact
// per-use types through the existing checker implementation.
func CheckFlow(ctx *project.CompilerContext, module *project.Module) *flowresult.Result {
	result := &flowresult.Result{
		SiteFacts:              make(map[ir.NodeID]map[cfg.SiteID]flowresult.Facts),
		ExprTypes:              make(map[ast.NodeID]typeinfo.Type),
		Payloads:               make(map[ast.NodeID]flowresult.PayloadAccess),
		VariantTests:           make(map[ast.NodeID]flowresult.VariantTest),
		ResolvedStorageOrigins: make(map[ast.NodeID][]place.Origin),
		ResolvedValueOrigins:   make(map[ast.NodeID][]place.Origin),
	}
	if ctx == nil || module == nil || module.CFG == nil || module.Semantics == nil || module.ModuleScope == nil {
		return result
	}
	for _, graph := range module.CFG.Functions {
		if graph == nil {
			continue
		}
		fn, _ := module.TypedASTNodes[ast.NodeID(graph.NodeID)].(*ast.FnDecl)
		if fn == nil {
			continue
		}
		var sym *symbols.Symbol
		if fn.Receiver != nil {
			sym = module.Semantics.MethodSymbol[fn.ID()]
		} else if fn.Name != nil {
			sym, _ = module.ModuleScope.Lookup(fn.Name.Name)
		}
		if sym == nil || sym.Scope == nil {
			continue
		}
		fnType := typeinfo.FuncTypeFromDeclWithOptions(fn, project.TypeSyntaxOptions(ctx, module, nil, false))
		analyzer := &flowAnalyzer{
			ctx: ctx, module: module, functionScope: sym.Scope,
			graph: graph, result: result, sites: make(map[cfg.SiteID]*cfg.Site),
			inStates: make(map[cfg.SiteID]flowState),
		}
		if fnType != nil {
			analyzer.returnType = fnType.Return
		}
		analyzer.run()
	}
	return result
}

func (c *checker) typePayloadExpr(scope *symbols.Scope, expr ast.Expr, expected typeinfo.Type) typeinfo.Type {
	c.payloadContext++
	defer func() { c.payloadContext-- }()
	return c.typeExpr(scope, expr, expected)
}

func (c *checker) typeOptionalTestExpr(scope *symbols.Scope, expr ast.Expr, expected typeinfo.Type) typeinfo.Type {
	c.optionalTestContext++
	defer func() { c.optionalTestContext-- }()
	return c.typeExpr(scope, expr, expected)
}

func (c *checker) typeWholeCarrierExpr(scope *symbols.Scope, expr ast.Expr, expected typeinfo.Type) typeinfo.Type {
	previous := c.wholeCarrierExpr
	c.wholeCarrierExpr = expr
	defer func() { c.wholeCarrierExpr = previous }()
	return c.typeExpr(scope, expr, expected)
}

func (c *checker) effectiveExpressionType(scope *symbols.Scope, expr ast.Expr, base, expected typeinfo.Type) typeinfo.Type {
	if c == nil || expr == nil || base == nil {
		return base
	}
	resolution := place.Resolution{}
	if c.flow != nil {
		resolution = c.resolveFlowPlace(scope, expr, *c.flow.state)
	}
	if c.wholeCarrierExpr == expr {
		c.recordFlowResolution(expr, resolution)
		return base
	}
	if !isOptionalType(base) {
		c.recordFlowResolution(expr, resolution)
		return base
	}
	required := payloadDepthForExpected(base, expected)
	if c.payloadContext > 0 && required == 0 {
		required = optionalLayerCount(base)
	}
	if c.flow == nil {
		if c.optionalTestContext > 0 || required == 0 {
			return base
		}
		return unwrapOptionalLayers(base, required)
	}

	payloadCases := provenOptionalPayloadCases(c.flow.state.variants, resolution.StorageOrigins)
	resolved := unwrapOptionalLayers(base, len(payloadCases))
	applied := optionalLayerCount(base) - optionalLayerCount(resolved)
	payloadCases = payloadCases[:applied]
	if c.optionalTestContext == 0 {
		if _, explicitCarrier := typeinfo.Underlying(expected).(*typeinfo.OptionalType); explicitCarrier {
			c.recordFlowResolution(expr, resolution)
			return base
		}
	}
	if applied > 0 {
		c.recordPayloadAccess(expr, resolution, payloadCases)
	}
	valueOrigins := place.VariantPayloadOrigins(resolution.StorageOrigins, payloadCases)
	if _, _, reference := typeinfo.ReferenceValueTarget(resolved); reference {
		valueOrigins = place.CloneOrigins(resolution.ValueOrigins)
	} else if _, raw := typeinfo.Underlying(resolved).(*typeinfo.RawPtrType); raw {
		valueOrigins = place.CloneOrigins(resolution.ValueOrigins)
	}
	resolution.ValueOrigins = valueOrigins
	c.recordFlowResolution(expr, resolution)
	if c.optionalTestContext > 0 {
		return resolved
	}
	if required <= applied {
		return resolved
	}
	if place.IsPlaceExpr(expr) && !resolution.Stable {
		c.ctx.Diagnostics.Add(unstableOptionalNarrowingError(expr))
	} else {
		c.ctx.Diagnostics.Add(optionalPayloadProofError(expr))
	}
	return unwrapOptionalLayers(base, required)
}

func payloadDepthForExpected(src, expected typeinfo.Type) int {
	if src == nil || expected == nil {
		return 0
	}
	if _, optional := typeinfo.Underlying(expected).(*typeinfo.OptionalType); optional {
		return 0
	}
	current := src
	for depth := 1; ; depth++ {
		optional, ok := typeinfo.Underlying(current).(*typeinfo.OptionalType)
		if !ok || optional == nil || optional.Inner == nil {
			return 0
		}
		current = optional.Inner
		if typeinfo.Assignable(expected, current) {
			return depth
		}
	}
}

func optionalLayerCount(typ typeinfo.Type) int {
	depth := 0
	for {
		optional, ok := typeinfo.Underlying(typ).(*typeinfo.OptionalType)
		if !ok || optional == nil || optional.Inner == nil {
			return depth
		}
		depth++
		typ = optional.Inner
	}
}

func unwrapOptionalLayers(typ typeinfo.Type, depth int) typeinfo.Type {
	for range depth {
		optional, ok := typeinfo.Underlying(typ).(*typeinfo.OptionalType)
		if !ok || optional == nil || optional.Inner == nil {
			break
		}
		typ = optional.Inner
	}
	return typ
}

func (c *checker) recordOptionalTest(node *ast.BinaryExpr, subject ast.Expr) {
	if c == nil || node == nil || subject == nil {
		return
	}
	test := flowresult.OptionalTest{SubjectID: subject.ID(), PresentWhenTrue: node.Op == "!="}
	if c.flow == nil {
		if c.module != nil && c.module.Semantics != nil {
			c.module.Semantics.OptionalTests[node.ID()] = test
		}
		return
	}
	if payload, ok := c.flow.result.Payloads[subject.ID()]; ok {
		c.flow.result.VariantTests[node.ID()] = flowresult.VariantTest{
			SubjectID: subject.ID(), Case: ir.OptionalPresentCase,
			CaseWhenTrue: test.PresentWhenTrue, CaseCount: 2,
			PayloadPath: append([]int(nil), payload.Cases...),
		}
	} else {
		c.flow.result.VariantTests[node.ID()] = flowresult.VariantTest{
			SubjectID: subject.ID(), Case: ir.OptionalPresentCase,
			CaseWhenTrue: test.PresentWhenTrue, CaseCount: 2,
		}
	}
	if c.flow.events != nil {
		c.flow.events.next++
		c.flow.events.tests[node.ID()] = c.flow.events.next
	}
}

func (c *checker) recordPayloadAccess(expr ast.Expr, resolution place.Resolution, cases []int) {
	if c == nil || c.flow == nil || expr == nil || len(cases) == 0 {
		return
	}
	direct := len(resolution.StorageOrigins) == 1 &&
		resolution.StorageOrigins[0].Root != nil && len(resolution.StorageOrigins[0].Projections) == 0
	c.flow.result.Payloads[expr.ID()] = flowresult.PayloadAccess{
		CarrierOrigins: place.CloneOrigins(resolution.StorageOrigins),
		Cases:          append([]int(nil), cases...),
		Direct:         direct,
	}
}

func (c *checker) recordFlowResolution(expr ast.Expr, resolution place.Resolution) {
	if c == nil || c.flow == nil || expr == nil {
		return
	}
	id := expr.ID()
	c.flow.result.ResolvedStorageOrigins[id] = place.CloneOrigins(resolution.StorageOrigins)
	c.flow.result.ResolvedValueOrigins[id] = place.CloneOrigins(resolution.ValueOrigins)
}

func (c *checker) resolveFlowPlace(scope *symbols.Scope, expr ast.Expr, st flowState) place.Resolution {
	if c == nil || c.module == nil || c.module.Semantics == nil {
		return place.Resolution{}
	}
	return place.Resolve(scope, expr, place.ResolveOptions{
		ExprType: func(node ast.Expr) typeinfo.Type {
			if node == nil {
				return nil
			}
			if c.flow != nil {
				if typ := c.flow.result.ExprTypes[node.ID()]; typ != nil {
					return typ
				}
			}
			return c.module.Semantics.ExprTypes[node.ID()]
		},
		ResolveBinding: c.expandedDefaultBinding,
		ReferenceOrigins: func(sym *symbols.Symbol) []place.Origin {
			return st.references[sym]
		},
		RawPointerOrigins: func(sym *symbols.Symbol) []place.Origin {
			return st.rawPointers[sym]
		},
		CallOrigins: func(call *ast.CallExpr) []place.Origin {
			if call == nil || call.Callee == nil {
				return nil
			}
			calleeType := c.module.Semantics.ExprTypes[call.Callee.ID()]
			if c.flow != nil && c.flow.result.ExprTypes[call.Callee.ID()] != nil {
				calleeType = c.flow.result.ExprTypes[call.Callee.ID()]
			}
			fn, _ := typeinfo.Underlying(calleeType).(*typeinfo.FuncType)
			var origins []place.Origin
			for _, source := range typeinfo.ReturnOriginSources(call, fn) {
				origins = place.MergeOrigins(origins, c.resolveFlowPlace(scope, source, st).ValueOrigins)
			}
			return origins
		},
		ConstantIndex: func(index ast.Expr) (string, bool) {
			expected := c.module.Semantics.ExprTypes[index.ID()]
			if !typeinfo.IsIntegral(expected) {
				expected = typeinfo.DefaultIntegerType()
			}
			value, evaluated := consteval.EvaluateExpr(c.ctx, c.module, scope, index, expected)
			integer, integral := value.(*constvalue.IntConst)
			if !evaluated || !integral || integer == nil {
				return "", false
			}
			return integer.Text(), true
		},
		PayloadCases: func(base ast.Expr) []int {
			if c.flow == nil || base == nil {
				return nil
			}
			return c.flow.result.Payloads[base.ID()].Cases
		},
	})
}

func (a *flowAnalyzer) run() {
	if a == nil || a.graph == nil || a.graph.Entry == nil || len(a.graph.Entry.Sites) == 0 {
		return
	}
	order := make([]cfg.SiteID, 0)
	disconnected := make(map[cfg.SiteID]bool)
	for _, block := range a.graph.Blocks {
		if block == nil {
			continue
		}
		for _, site := range block.Sites {
			if site != nil {
				a.sites[site.ID] = site
				order = append(order, site.ID)
				disconnected[site.ID] = !block.Reachable
			}
		}
	}
	entryState := newFlowState()
	for _, sym := range a.functionScope.Symbols() {
		if sym == nil || sym.Kind != symbols.SymbolParam {
			continue
		}
		if typ, ok := symbols.GetSymbolType(sym); ok {
			if _, _, reference := typeinfo.ReferenceValueTarget(typ); reference {
				entryState.references[sym] = []place.Origin{{Root: sym}}
			}
		}
	}
	entry := a.graph.Entry.Sites[0].ID
	a.inStates[entry] = entryState
	queue := []cfg.SiteID{entry}
	queued := map[cfg.SiteID]bool{entry: true}
	for _, id := range order {
		if id == entry || len(a.sites[id].Predecessors) != 0 {
			continue
		}
		a.inStates[id] = copyFlowState(entryState)
		queue = append(queue, id)
		queued[id] = true
	}
	for {
		if len(queue) == 0 {
			for _, id := range order {
				if _, visited := a.inStates[id]; visited || !disconnected[id] {
					continue
				}
				a.inStates[id] = copyFlowState(entryState)
				queue = append(queue, id)
				queued[id] = true
				break
			}
			if len(queue) == 0 {
				break
			}
		}
		id := queue[0]
		queue = queue[1:]
		queued[id] = false
		site := a.sites[id]
		if site == nil {
			continue
		}
		input := copyFlowState(a.inStates[id])
		if a.result.SiteFacts[a.graph.NodeID] == nil {
			a.result.SiteFacts[a.graph.NodeID] = make(map[cfg.SiteID]flowresult.Facts)
		}
		a.result.SiteFacts[a.graph.NodeID][id] = snapshotFlowState(input)
		next := copyFlowState(input)
		events := a.applySite(site, &next)
		for _, edge := range site.Successors {
			if a.sites[edge.To] == nil {
				continue
			}
			out := copyFlowState(next)
			if site.Kind == cfg.SiteTerminator {
				a.applyConditionEdge(site, edge.Kind, &out, events)
			}
			if !out.reachable {
				continue
			}
			current, exists := a.inStates[edge.To]
			merged := out
			if exists {
				merged = mergeFlowStates(current, out)
			}
			if exists && sameFlowState(current, merged) {
				continue
			}
			a.inStates[edge.To] = merged
			if !queued[edge.To] {
				queue = append(queue, edge.To)
				queued[edge.To] = true
			}
		}
	}
}

func newFlowState() flowState {
	return flowState{
		reachable:   true,
		references:  make(map[*symbols.Symbol][]place.Origin),
		rawPointers: make(map[*symbols.Symbol][]place.Origin),
	}
}

func copyFlowState(src flowState) flowState {
	dst := newFlowState()
	dst.reachable = src.reachable
	for _, fact := range src.variants {
		dst.variants = append(dst.variants, variantStateFact{
			origins: place.CloneOrigins(fact.origins), cases: append([]int(nil), fact.cases...), caseCount: fact.caseCount,
			dependencies: append([]*symbols.Symbol(nil), fact.dependencies...),
		})
	}
	for sym, origins := range src.references {
		dst.references[sym] = place.CloneOrigins(origins)
	}
	for sym, origins := range src.rawPointers {
		dst.rawPointers[sym] = place.CloneOrigins(origins)
	}
	return dst
}

func snapshotFlowState(st flowState) flowresult.Facts {
	facts := flowresult.Facts{
		ReferenceOrigins:  make(map[symbols.SymbolID][]place.Origin),
		RawPointerOrigins: make(map[symbols.SymbolID][]place.Origin),
	}
	for _, fact := range st.variants {
		dependencies := make([]symbols.SymbolID, 0, len(fact.dependencies))
		for _, sym := range fact.dependencies {
			if sym != nil {
				dependencies = append(dependencies, sym.ID)
			}
		}
		facts.Variants = append(facts.Variants, flowresult.VariantFact{
			CarrierOrigins: place.CloneOrigins(fact.origins), Cases: append([]int(nil), fact.cases...),
			CaseCount: fact.caseCount, Dependencies: dependencies,
		})
	}
	for sym, origins := range st.references {
		if sym != nil {
			facts.ReferenceOrigins[sym.ID] = place.CloneOrigins(origins)
		}
	}
	for sym, origins := range st.rawPointers {
		if sym != nil {
			facts.RawPointerOrigins[sym.ID] = place.CloneOrigins(origins)
		}
	}
	return facts
}

func (a *flowAnalyzer) applySite(site *cfg.Site, st *flowState) *flowExpressionEvents {
	events := &flowExpressionEvents{tests: make(map[ast.NodeID]int)}
	if site == nil || st == nil {
		return events
	}
	scope := a.module.Semantics.BlockScopes[ast.NodeID(site.ScopeID)]
	if scope == nil {
		scope = a.functionScope
	}
	checker := &checker{
		ctx: a.ctx, module: a.module, siteOnly: true,
		flow: &flowCheck{result: a.result, state: st, analyzer: a, events: events},
	}
	switch site.Kind {
	case cfg.SiteStatement, cfg.SiteTerminator:
		stmt, _ := a.module.TypedASTNodes[ast.NodeID(site.NodeID)].(ast.Stmt)
		if stmt == nil {
			return events
		}
		checker.checkStmt(scope, stmt, a.returnType)
		a.applyStatementEffects(checker, scope, stmt, st)
	case cfg.SiteScopeExit:
		block, _ := a.module.TypedASTNodes[ast.NodeID(site.NodeID)].(*ast.BlockStmt)
		if block != nil {
			blockScope := a.module.Semantics.BlockScopes[block.ID()]
			if blockScope == nil {
				return events
			}
			clearFlowScope(blockScope, st)
		}
	}
	return events
}

func (a *flowAnalyzer) applyStatementEffects(c *checker, scope *symbols.Scope, stmt ast.Stmt, st *flowState) {
	if c == nil || stmt == nil || st == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.LetDecl:
		if sym, found := scope.LookupNode(node); found {
			a.updateOriginBinding(c, scope, sym, node.Value, st)
		}
	case *ast.ConstDecl:
		if sym, found := scope.LookupNode(node); found {
			a.updateOriginBinding(c, scope, sym, node.Value, st)
		}
	case *ast.AssignStmt:
		resolution := c.resolveFlowPlace(scope, node.Target, *st)
		invalidateVariantOrigins(st, resolution.StorageOrigins)
		if sym := a.assignedSymbol(scope, node.Target); sym != nil {
			invalidateVariantDependency(st, sym)
			a.updateOriginBinding(c, scope, sym, node.Value, st)
		}
	}
}

func (a *flowAnalyzer) assignedSymbol(scope *symbols.Scope, expr ast.Expr) *symbols.Symbol {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}
	if sym := a.module.Semantics.ResolvedSymbols[ident.ID()]; sym != nil {
		return sym
	}
	sym, _ := scope.Lookup(ident.Name)
	return sym
}

func (a *flowAnalyzer) updateOriginBinding(c *checker, scope *symbols.Scope, sym *symbols.Symbol, value ast.Expr, st *flowState) {
	if sym == nil || st == nil {
		return
	}
	typ, typed := symbols.GetSymbolType(sym)
	if !typed {
		delete(st.references, sym)
		delete(st.rawPointers, sym)
		return
	}
	if _, _, reference := typeinfo.ReferenceValueTarget(typ); reference {
		st.references[sym] = place.CloneOrigins(c.resolveFlowPlace(scope, value, *st).ValueOrigins)
	} else {
		delete(st.references, sym)
	}
	if _, raw := typeinfo.Underlying(typ).(*typeinfo.RawPtrType); raw {
		if origins, known := a.rawPointerOrigins(c, scope, value, *st); known {
			st.rawPointers[sym] = origins
		} else {
			delete(st.rawPointers, sym)
		}
	} else {
		delete(st.rawPointers, sym)
	}
}

func (a *flowAnalyzer) invalidateCall(c *checker, scope *symbols.Scope, call *ast.CallExpr, st *flowState) {
	if call == nil || call.Callee == nil || st == nil {
		return
	}
	calleeType := a.result.ExprTypes[call.Callee.ID()]
	if calleeType == nil {
		calleeType = a.module.Semantics.ExprTypes[call.Callee.ID()]
	}
	fn, _ := typeinfo.Underlying(calleeType).(*typeinfo.FuncType)
	args := append([]ast.Expr(nil), call.Args...)
	if selector, method := call.Callee.(*ast.SelectorExpr); method && selector != nil {
		args = append([]ast.Expr{selector.Expr}, args...)
	}
	if fn != nil && len(fn.Params) == len(args) {
		for index, arg := range args {
			param := fn.Params[index]
			if _, mutable, reference := typeinfo.ReferenceValueTarget(param); reference && mutable {
				invalidateVariantOrigins(st, c.resolveFlowPlace(scope, arg, *st).ValueOrigins)
			}
			if _, raw := typeinfo.Underlying(param).(*typeinfo.RawPtrType); raw {
				if origins, known := a.rawPointerOrigins(c, scope, arg, *st); known {
					invalidateVariantOrigins(st, origins)
				} else {
					st.variants = nil
				}
			}
		}
	}
	for _, sym := range a.module.ModuleScope.Symbols() {
		if sym != nil && sym.IsMutable() {
			invalidateVariantOrigins(st, []place.Origin{{Root: sym}})
		}
	}
}

func (a *flowAnalyzer) rawPointerOrigins(c *checker, scope *symbols.Scope, expr ast.Expr, st flowState) ([]place.Origin, bool) {
	switch node := expr.(type) {
	case *ast.AddressExpr:
		if node != nil && node.Mode == ast.AddressRaw {
			origins := c.resolveFlowPlace(scope, node.Expr, st).ValueOrigins
			return origins, len(origins) > 0
		}
	case *ast.Ident:
		if sym := a.assignedSymbol(scope, node); sym != nil {
			origins, known := st.rawPointers[sym]
			return place.CloneOrigins(origins), known
		}
	case *ast.AsExpr:
		return a.rawPointerOrigins(c, scope, node.Expr, st)
	}
	return nil, false
}

func (a *flowAnalyzer) applyConditionEdge(site *cfg.Site, edge cfg.EdgeKind, st *flowState, events *flowExpressionEvents) {
	if st == nil || (edge != cfg.EdgeTrue && edge != cfg.EdgeFalse) {
		return
	}
	stmt, _ := a.module.TypedASTNodes[ast.NodeID(site.NodeID)].(ast.Stmt)
	var condition ast.Expr
	switch node := stmt.(type) {
	case *ast.IfStmt:
		condition = node.Cond
	case *ast.ForStmt:
		condition = node.Cond
	}
	if condition == nil {
		return
	}
	scope := a.module.Semantics.BlockScopes[ast.NodeID(site.ScopeID)]
	if scope == nil {
		scope = a.functionScope
	}
	for _, implied := range a.impliedVariants(scope, condition, edge == cfg.EdgeTrue, *st, events) {
		filtered := copyFlowState(*st)
		filtered.variants = nil
		restrictVariantFact(&filtered, implied.variant)
		checker := &checker{ctx: a.ctx, module: a.module, flow: &flowCheck{result: a.result, state: &filtered}}
		for _, call := range events.calls {
			if call.order > implied.order {
				a.invalidateCall(checker, scope, call.call, &filtered)
			}
		}
		if !filtered.reachable {
			st.reachable = false
			return
		}
		if len(filtered.variants) > 0 {
			restrictVariantFact(st, filtered.variants[0])
		}
	}
}

func (a *flowAnalyzer) impliedVariants(
	scope *symbols.Scope,
	expr ast.Expr,
	truth bool,
	st flowState,
	events *flowExpressionEvents,
) []edgeVariantFact {
	if expr == nil {
		return nil
	}
	if test, found := a.result.VariantTests[expr.ID()]; found {
		subject, _ := a.module.TypedASTNodes[test.SubjectID].(ast.Expr)
		checker := &checker{ctx: a.ctx, module: a.module, flow: &flowCheck{result: a.result, state: &st}}
		resolution := checker.resolveFlowPlace(scope, subject, st)
		if !resolution.Stable || len(resolution.StorageOrigins) == 0 {
			a.ctx.Diagnostics.Add(unstableOptionalNarrowingError(subject))
			return nil
		}
		cases := []int{test.Case}
		if truth != test.CaseWhenTrue {
			cases = variantCasesExcept(test.CaseCount, test.Case)
		}
		order := 0
		if events != nil {
			order = events.tests[expr.ID()]
		}
		return []edgeVariantFact{{
			variant: variantStateFact{
				origins: place.VariantPayloadOrigins(resolution.StorageOrigins, test.PayloadPath),
				cases:   cases, caseCount: test.CaseCount,
				dependencies: append([]*symbols.Symbol(nil), resolution.Dependencies...),
			},
			order: order,
		}}
	}
	switch node := expr.(type) {
	case *ast.UnaryExpr:
		if node.Op == "!" {
			return a.impliedVariants(scope, node.Expr, !truth, st, events)
		}
	case *ast.BinaryExpr:
		switch node.Op {
		case "&&":
			if truth {
				return a.constrainEdgeVariantFacts(scope, st, events,
					a.impliedVariants(scope, node.Left, true, st, events),
					a.impliedVariants(scope, node.Right, true, st, events),
				)
			}
			return alternateEdgeVariantFacts(
				a.impliedVariants(scope, node.Left, false, st, events),
				a.impliedVariants(scope, node.Right, false, st, events),
			)
		case "||":
			if truth {
				return alternateEdgeVariantFacts(
					a.impliedVariants(scope, node.Left, true, st, events),
					a.impliedVariants(scope, node.Right, true, st, events),
				)
			}
			return a.constrainEdgeVariantFacts(scope, st, events,
				a.impliedVariants(scope, node.Left, false, st, events),
				a.impliedVariants(scope, node.Right, false, st, events),
			)
		}
	}
	return nil
}

func provenOptionalPayloadCases(facts []variantStateFact, origins []place.Origin) []int {
	path := make([]int, 0)
	current := place.CloneOrigins(origins)
	for {
		fact, found := variantFact(facts, current)
		if !found || !sameCaseSet(fact.cases, []int{ir.OptionalPresentCase}) {
			return path
		}
		path = append(path, ir.OptionalPresentCase)
		current = place.VariantPayloadOrigins(current, []int{ir.OptionalPresentCase})
	}
}

func restrictVariantFact(st *flowState, added variantStateFact) {
	if st == nil || !st.reachable || len(added.origins) == 0 || added.caseCount <= 0 {
		return
	}
	if len(added.cases) == 0 {
		st.reachable = false
		st.variants = nil
		return
	}
	if len(added.cases) >= added.caseCount {
		return
	}
	for index := range st.variants {
		if place.SameOrigins(st.variants[index].origins, added.origins) {
			if st.variants[index].caseCount != added.caseCount {
				st.reachable = false
				st.variants = nil
				return
			}
			st.variants[index].cases = intersectCaseSets(st.variants[index].cases, added.cases)
			if len(st.variants[index].cases) == 0 {
				st.reachable = false
				st.variants = nil
				return
			}
			st.variants[index].dependencies = mergeDependencies(st.variants[index].dependencies, added.dependencies)
			return
		}
	}
	st.variants = append(st.variants, variantStateFact{
		origins: place.CloneOrigins(added.origins), cases: append([]int(nil), added.cases...), caseCount: added.caseCount,
		dependencies: append([]*symbols.Symbol(nil), added.dependencies...),
	})
}

func (a *flowAnalyzer) constrainEdgeVariantFacts(
	scope *symbols.Scope,
	st flowState,
	events *flowExpressionEvents,
	left, right []edgeVariantFact,
) []edgeVariantFact {
	merged := make([]edgeVariantFact, len(left))
	for i, fact := range left {
		merged[i] = edgeVariantFact{variant: variantStateFact{
			origins: place.CloneOrigins(fact.variant.origins), cases: append([]int(nil), fact.variant.cases...),
			caseCount: fact.variant.caseCount, dependencies: append([]*symbols.Symbol(nil), fact.variant.dependencies...),
		}, order: fact.order}
	}
	for _, candidate := range right {
		found := false
		for index := range merged {
			if !place.SameOrigins(merged[index].variant.origins, candidate.variant.origins) {
				continue
			}
			found = true
			if a.variantFactInvalidatedBetween(scope, st, events, merged[index], candidate.order) {
				candidate.variant.origins = place.CloneOrigins(candidate.variant.origins)
				candidate.variant.cases = append([]int(nil), candidate.variant.cases...)
				candidate.variant.dependencies = append([]*symbols.Symbol(nil), candidate.variant.dependencies...)
				merged[index] = candidate
				break
			}
			merged[index].variant.cases = intersectCaseSets(merged[index].variant.cases, candidate.variant.cases)
			merged[index].variant.dependencies = mergeDependencies(
				merged[index].variant.dependencies, candidate.variant.dependencies,
			)
			merged[index].order = max(merged[index].order, candidate.order)
			break
		}
		if !found {
			candidate.variant.origins = place.CloneOrigins(candidate.variant.origins)
			candidate.variant.cases = append([]int(nil), candidate.variant.cases...)
			candidate.variant.dependencies = append([]*symbols.Symbol(nil), candidate.variant.dependencies...)
			merged = append(merged, candidate)
		}
	}
	return merged
}

func (a *flowAnalyzer) variantFactInvalidatedBetween(
	scope *symbols.Scope,
	st flowState,
	events *flowExpressionEvents,
	fact edgeVariantFact,
	before int,
) bool {
	if a == nil || events == nil || before <= fact.order {
		return false
	}
	filtered := copyFlowState(st)
	filtered.variants = nil
	restrictVariantFact(&filtered, fact.variant)
	if !filtered.reachable {
		return false
	}
	checker := &checker{ctx: a.ctx, module: a.module, flow: &flowCheck{result: a.result, state: &filtered}}
	for _, call := range events.calls {
		if call.order > fact.order && call.order < before {
			a.invalidateCall(checker, scope, call.call, &filtered)
		}
	}
	_, found := variantFact(filtered.variants, fact.variant.origins)
	return !found
}

func alternateEdgeVariantFacts(left, right []edgeVariantFact) []edgeVariantFact {
	out := make([]edgeVariantFact, 0)
	for _, leftFact := range left {
		for _, rightFact := range right {
			if !place.SameOrigins(leftFact.variant.origins, rightFact.variant.origins) || leftFact.variant.caseCount != rightFact.variant.caseCount {
				continue
			}
			cases := unionCaseSets(leftFact.variant.cases, rightFact.variant.cases)
			if len(cases) >= leftFact.variant.caseCount {
				break
			}
			out = append(out, edgeVariantFact{
				variant: variantStateFact{
					origins: place.CloneOrigins(leftFact.variant.origins), cases: cases, caseCount: leftFact.variant.caseCount,
					dependencies: mergeDependencies(
						leftFact.variant.dependencies, rightFact.variant.dependencies,
					),
				},
				order: min(leftFact.order, rightFact.order),
			})
			break
		}
	}
	return out
}

func mergeVariantFacts(left, right []variantStateFact) []variantStateFact {
	out := make([]variantStateFact, 0)
	for _, leftFact := range left {
		for _, rightFact := range right {
			if !place.SameOrigins(leftFact.origins, rightFact.origins) || leftFact.caseCount != rightFact.caseCount {
				continue
			}
			cases := unionCaseSets(leftFact.cases, rightFact.cases)
			if len(cases) >= leftFact.caseCount {
				break
			}
			out = append(out, variantStateFact{
				origins: place.CloneOrigins(leftFact.origins), cases: cases, caseCount: leftFact.caseCount,
				dependencies: mergeDependencies(leftFact.dependencies, rightFact.dependencies),
			})
			break
		}
	}
	return out
}

func mergeFlowStates(left, right flowState) flowState {
	if !left.reachable {
		return copyFlowState(right)
	}
	if !right.reachable {
		return copyFlowState(left)
	}
	merged := newFlowState()
	merged.variants = mergeVariantFacts(left.variants, right.variants)
	for sym, origins := range left.references {
		merged.references[sym] = place.CloneOrigins(origins)
	}
	for sym, origins := range right.references {
		merged.references[sym] = place.MergeOrigins(merged.references[sym], origins)
	}
	for sym, origins := range left.rawPointers {
		merged.rawPointers[sym] = place.CloneOrigins(origins)
	}
	for sym, origins := range right.rawPointers {
		merged.rawPointers[sym] = place.MergeOrigins(merged.rawPointers[sym], origins)
	}
	return merged
}

func sameFlowState(left, right flowState) bool {
	if left.reachable != right.reachable || len(left.variants) != len(right.variants) || len(left.references) != len(right.references) ||
		len(left.rawPointers) != len(right.rawPointers) {
		return false
	}
	for _, fact := range left.variants {
		rightFact, found := variantFact(right.variants, fact.origins)
		if !found || rightFact.caseCount != fact.caseCount || !sameCaseSet(rightFact.cases, fact.cases) {
			return false
		}
	}
	if !maps.EqualFunc(left.references, right.references, place.SameOrigins) ||
		!maps.EqualFunc(left.rawPointers, right.rawPointers, place.SameOrigins) {
		return false
	}
	return true
}

func variantFact(facts []variantStateFact, origins []place.Origin) (variantStateFact, bool) {
	for _, fact := range facts {
		if place.SameOrigins(fact.origins, origins) {
			return fact, true
		}
	}
	return variantStateFact{}, false
}

func variantCasesExcept(caseCount, excluded int) []int {
	cases := make([]int, 0, max(caseCount-1, 0))
	for caseIndex := range caseCount {
		if caseIndex != excluded {
			cases = append(cases, caseIndex)
		}
	}
	return cases
}

func sameCaseSet(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for _, candidate := range left {
		if !containsCase(right, candidate) {
			return false
		}
	}
	return true
}

func intersectCaseSets(left, right []int) []int {
	out := make([]int, 0, min(len(left), len(right)))
	for _, candidate := range left {
		if containsCase(right, candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func unionCaseSets(left, right []int) []int {
	out := append([]int(nil), left...)
	for _, candidate := range right {
		if !containsCase(out, candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func containsCase(cases []int, candidate int) bool {
	for _, caseIndex := range cases {
		if caseIndex == candidate {
			return true
		}
	}
	return false
}

func mergeDependencies(left, right []*symbols.Symbol) []*symbols.Symbol {
	merged := append([]*symbols.Symbol(nil), left...)
	for _, candidate := range right {
		found := false
		for _, existing := range merged {
			if existing == candidate {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, candidate)
		}
	}
	return merged
}

func invalidateVariantDependency(st *flowState, assigned *symbols.Symbol) {
	if st == nil || assigned == nil {
		return
	}
	kept := st.variants[:0]
	for _, fact := range st.variants {
		dependent := false
		for _, dependency := range fact.dependencies {
			if dependency == assigned {
				dependent = true
				break
			}
		}
		if !dependent {
			kept = append(kept, fact)
		}
	}
	st.variants = kept
}

func invalidateVariantOrigins(st *flowState, mutated []place.Origin) {
	if st == nil || len(mutated) == 0 {
		return
	}
	kept := st.variants[:0]
	for _, fact := range st.variants {
		if !place.OriginsOverlap(fact.origins, mutated) {
			kept = append(kept, fact)
			continue
		}
		preserved := true
		for _, mutation := range mutated {
			for _, carrier := range fact.origins {
				if !place.OriginsOverlap([]place.Origin{carrier}, []place.Origin{mutation}) {
					continue
				}
				if !mutationPreservesVariantCase(carrier, mutation) {
					preserved = false
				}
			}
		}
		if preserved {
			kept = append(kept, fact)
		}
	}
	st.variants = kept
}

func mutationPreservesVariantCase(carrier, mutation place.Origin) bool {
	if carrier.Root == nil || carrier.Root != mutation.Root || len(mutation.Projections) <= len(carrier.Projections) {
		return false
	}
	for index := range carrier.Projections {
		if carrier.Projections[index] != mutation.Projections[index] {
			return false
		}
	}
	return mutation.Projections[len(carrier.Projections)].Kind == place.OriginVariantPayload
}

func clearFlowScope(scope *symbols.Scope, st *flowState) {
	if scope == nil || st == nil {
		return
	}
	for _, sym := range scope.Symbols() {
		delete(st.references, sym)
		delete(st.rawPointers, sym)
		invalidateVariantDependency(st, sym)
		invalidateVariantOrigins(st, []place.Origin{{Root: sym}})
	}
}
