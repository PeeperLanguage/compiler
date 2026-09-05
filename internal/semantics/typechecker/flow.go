package typechecker

import (
	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	graphcore "compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/project"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

type variantStateFact struct {
	origins      []place.Origin
	cases        []int
	caseCount    int
	dependencies []*symbols.Symbol
}

type originStateFact struct {
	storage []place.Origin
	value   []place.Origin
}

type flowState struct {
	reachable   bool
	variants    []variantStateFact
	references  []originStateFact
	rawPointers []originStateFact
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

// CheckFlow runs variant/origin facts to fixed point, then records exact
// per-use types through the existing checker implementation.
func CheckFlow(ctx *project.CompilerContext, module *project.Module) *flowresult.Result {
	result := &flowresult.Result{
		SiteFacts:              make(map[ir.NodeID]map[cfg.SiteID]flowresult.Facts),
		ExprTypes:              make(map[ast.NodeID]typeinfo.Type),
		Payloads:               make(map[ast.NodeID]flowresult.PayloadAccess),
		CaseTests:              make(map[ast.NodeID]flowresult.CaseTest),
		VariantFields:          make(map[ast.NodeID]flowresult.VariantFieldAccess),
		ResolvedStorageOrigins: make(map[ast.NodeID][]place.Origin),
		ResolvedValueOrigins:   make(map[ast.NodeID][]place.Origin),
	}
	if ctx == nil || module == nil || module.CFG == nil || module.Bindings == nil || module.ModuleScope == nil {
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
			sym = module.Bindings.MethodsByDecl[fn.ID()]
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
	_, explicitCarrier := typeinfo.Underlying(expected).(*typeinfo.OptionalType)
	required := payloadDepthForExpected(base, expected)
	if c.payloadContext > 0 && required == 0 && !explicitCarrier {
		required = optionalLayerCount(base)
	}
	if c.flow == nil {
		if c.optionalTestContext > 0 || explicitCarrier || required == 0 {
			return base
		}
		return unwrapOptionalLayers(base, required)
	}

	payloadCases := provenOptionalPayloadCases(c.flow.state.variants, resolution.StorageOrigins)
	resolved := unwrapOptionalLayers(base, len(payloadCases))
	applied := optionalLayerCount(base) - optionalLayerCount(resolved)
	payloadCases = payloadCases[:applied]
	if c.optionalTestContext == 0 && explicitCarrier {
		c.recordFlowResolution(expr, resolution)
		return base
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

func (c *checker) recordCaseTest(node ast.Expr, subject ast.Expr, caseIndex, caseCount int, caseWhenTrue bool, family typeinfo.VariantFamily) {
	if c == nil || node == nil || subject == nil {
		return
	}
	test := typecheckresult.CaseTest{
		SubjectID: subject.ID(), Case: caseIndex, CaseWhenTrue: caseWhenTrue,
		CaseCount: caseCount, Family: family,
	}
	if c.flow == nil {
		if c.module != nil && c.module.Typechecking != nil {
			c.module.Typechecking.CaseTests[node.ID()] = test
		}
		return
	}
	base, found := c.module.Typechecking.CaseTests[node.ID()]
	if !found || base.SubjectID != subject.ID() {
		return
	}
	refined := flowresult.CaseTest{CaseTest: base}
	if payload, ok := c.flow.result.Payloads[subject.ID()]; ok {
		storage := c.flow.result.ResolvedStorageOrigins[subject.ID()]
		if payload.AppliesTo(storage) {
			refined.PayloadPath = append([]int(nil), payload.Cases...)
		}
	}
	c.flow.result.CaseTests[node.ID()] = refined
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
	if c == nil || c.module == nil {
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
			return c.module.BaseExprType(node.ID())
		},
		ResolveBinding: c.module.ExpandedDefaultBinding,
		ReferenceOrigins: func(storage []place.Origin) []place.Origin {
			return originValues(st.references, storage)
		},
		RawPointerOrigins: func(storage []place.Origin) []place.Origin {
			return originValues(st.rawPointers, storage)
		},
		CallOrigins: func(call *ast.CallExpr) []place.Origin {
			if call == nil || call.Callee == nil {
				return nil
			}
			calleeType := c.module.BaseExprType(call.Callee.ID())
			if c.flow != nil && c.flow.result.ExprTypes[call.Callee.ID()] != nil {
				calleeType = c.flow.result.ExprTypes[call.Callee.ID()]
			}
			fn, _ := typeinfo.Underlying(calleeType).(*typeinfo.FuncType)
			var origins []place.Origin
			args := c.module.Typechecking.CallArgumentsOrSource(call)
			for _, source := range typeinfo.ReturnOriginSources(call, args, fn) {
				origins = place.MergeOrigins(origins, c.resolveFlowPlace(scope, source, st).ValueOrigins)
			}
			return origins
		},
		ConstantIndex: func(index ast.Expr) (string, bool) {
			expected := c.module.BaseExprType(index.ID())
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
				carrier := []place.Origin{{Root: sym}}
				cases := make([]int, optionalLayerCount(typ))
				for index := range cases {
					cases[index] = ir.OptionalPresentCase
				}
				value := place.VariantPayloadOrigins(carrier, cases)
				entryState.references = setOriginFact(entryState.references, carrier, value)
			}
		}
	}
	entry := a.graph.Entry.Sites[0].ID
	a.inStates[entry] = entryState
	work := graphcore.NewWorklist(entry)
	for _, id := range order {
		if id == entry || a.graph.SiteEdges.InDegree(id, nil) != 0 {
			continue
		}
		a.inStates[id] = copyFlowState(entryState)
		work.Add(id)
	}
	for {
		id, pending := work.Next()
		if !pending {
			seeded := false
			for _, id := range order {
				if _, visited := a.inStates[id]; visited || !disconnected[id] {
					continue
				}
				a.inStates[id] = copyFlowState(entryState)
				work.Add(id)
				seeded = true
				break
			}
			if !seeded {
				break
			}
			continue
		}
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
		for _, edge := range a.graph.SiteEdges.OutEdges(site.ID) {
			if a.sites[edge.To] == nil {
				continue
			}
			out := copyFlowState(next)
			if site.Kind == cfg.SiteTerminator {
				a.applyConditionEdge(site, edge.Kind, &out, events)
				a.applyVariantCaseEdge(site, edge, &out)
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
			work.Add(edge.To)
		}
	}
}

func (a *flowAnalyzer) applyVariantCaseEdge(site *cfg.Site, edge cfg.Edge, st *flowState) {
	if a == nil || site == nil || st == nil || edge.Kind != cfg.EdgeVariantCase {
		return
	}
	match, found := a.module.Typechecking.Matches[ast.NodeID(site.NodeID)]
	if !found {
		return
	}
	subject, _ := a.module.TypedASTNodes[match.SubjectID].(ast.Expr)
	if subject == nil {
		return
	}
	scope := a.module.Bindings.BlockScopes[ast.NodeID(site.ScopeID)]
	if scope == nil {
		scope = a.functionScope
	}
	checker := &checker{ctx: a.ctx, module: a.module, flow: &flowCheck{result: a.result, state: st}}
	resolution := checker.resolveFlowPlace(scope, subject, *st)
	if resolution.Stable && len(resolution.StorageOrigins) > 0 {
		restrictVariantFact(st, variantStateFact{
			origins:      resolution.StorageOrigins,
			cases:        []int{edge.Case},
			caseCount:    match.CaseCount,
			dependencies: append([]*symbols.Symbol(nil), resolution.Dependencies...),
		})
	}
	arm, found := match.Arm(edge.Case)
	if !found || arm.Payload == nil {
		return
	}
	payloadOrigins := place.VariantPayloadOrigins(resolution.ValueOrigins, []int{edge.Case})
	for _, field := range arm.Bindings {
		fieldOrigins := payloadOrigins
		switch field.Projection {
		case typecheckresult.MatchPayloadField:
			payload, payloadFound := typeinfo.Underlying(arm.Payload).(*typeinfo.StructType)
			if !payloadFound || payload == nil || field.Field < 0 || field.Field >= len(payload.Fields) {
				continue
			}
			fieldOrigins = place.FieldOrigins(payloadOrigins, payload.Fields[field.Field].Name)
		case typecheckresult.MatchWholePayload:
		default:
			panic("flow typechecking: invalid match binding projection")
		}
		if field.Binding == nil {
			continue
		}
		bindingOrigins := []place.Origin{{Root: field.Binding}}
		valueOrigins := fieldOrigins
		if _, _, reference := typeinfo.ReferenceValueTarget(field.Type); reference {
			valueOrigins = originValues(st.references, fieldOrigins)
			if len(valueOrigins) == 0 {
				valueOrigins = fieldOrigins
			}
			st.references = setOriginFact(st.references, bindingOrigins, valueOrigins)
		}
		if _, raw := typeinfo.Underlying(field.Type).(*typeinfo.RawPtrType); raw {
			valueOrigins = originValues(st.rawPointers, fieldOrigins)
			if len(valueOrigins) == 0 {
				valueOrigins = fieldOrigins
			}
			st.rawPointers = setOriginFact(st.rawPointers, bindingOrigins, valueOrigins)
		}
		if field.Binding.ASTNode != nil {
			id := field.Binding.ASTNode.ID()
			a.result.ResolvedStorageOrigins[id] = place.MergeOrigins(a.result.ResolvedStorageOrigins[id], bindingOrigins)
			a.result.ResolvedValueOrigins[id] = place.MergeOrigins(a.result.ResolvedValueOrigins[id], valueOrigins)
		}
	}
}

func newFlowState() flowState {
	return flowState{reachable: true}
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
	dst.references = cloneOriginFacts(src.references)
	dst.rawPointers = cloneOriginFacts(src.rawPointers)
	return dst
}

func snapshotFlowState(st flowState) flowresult.Facts {
	facts := flowresult.Facts{}
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
	for _, fact := range st.references {
		facts.ReferenceOrigins = append(facts.ReferenceOrigins, flowresult.OriginFact{
			StorageOrigins: place.CloneOrigins(fact.storage), ValueOrigins: place.CloneOrigins(fact.value),
		})
	}
	for _, fact := range st.rawPointers {
		facts.RawPointerOrigins = append(facts.RawPointerOrigins, flowresult.OriginFact{
			StorageOrigins: place.CloneOrigins(fact.storage), ValueOrigins: place.CloneOrigins(fact.value),
		})
	}
	return facts
}

func (a *flowAnalyzer) applySite(site *cfg.Site, st *flowState) *flowExpressionEvents {
	events := &flowExpressionEvents{tests: make(map[ast.NodeID]int)}
	if site == nil || st == nil {
		return events
	}
	scope := a.module.Bindings.BlockScopes[ast.NodeID(site.ScopeID)]
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
			blockScope := a.module.Bindings.BlockScopes[block.ID()]
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
			typ, _ := symbols.GetSymbolType(sym)
			a.updateOriginPlace(c, scope, []place.Origin{{Root: sym}}, typ, node.Value, copyFlowState(*st), st)
		}
	case *ast.ConstDecl:
		if sym, found := scope.LookupNode(node); found {
			typ, _ := symbols.GetSymbolType(sym)
			a.updateOriginPlace(c, scope, []place.Origin{{Root: sym}}, typ, node.Value, copyFlowState(*st), st)
		}
	case *ast.AssignStmt:
		sourceState := copyFlowState(*st)
		resolution := c.resolveFlowPlace(scope, node.Target, *st)
		invalidateVariantOrigins(st, resolution.StorageOrigins)
		typ := a.result.ExprTypes[node.Target.ID()]
		if typ == nil {
			typ = a.module.BaseExprType(node.Target.ID())
		}
		a.updateOriginPlace(c, scope, resolution.StorageOrigins, typ, node.Value, sourceState, st)
	}
}

func (a *flowAnalyzer) assignedSymbol(scope *symbols.Scope, expr ast.Expr) *symbols.Symbol {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident == nil {
		return nil
	}
	if sym := a.module.Bindings.NodeSymbols[ident.ID()]; sym != nil {
		return sym
	}
	sym, _ := scope.Lookup(ident.Name)
	return sym
}

func (a *flowAnalyzer) updateOriginPlace(
	c *checker,
	scope *symbols.Scope,
	storage []place.Origin,
	typ typeinfo.Type,
	value ast.Expr,
	sourceState flowState,
	st *flowState,
) {
	if st == nil || len(storage) == 0 {
		return
	}
	st.references = invalidateOriginFacts(st.references, storage)
	st.rawPointers = invalidateOriginFacts(st.rawPointers, storage)
	if _, _, reference := typeinfo.ReferenceValueTarget(typ); reference {
		st.references = setOriginFact(st.references, storage, c.resolveFlowPlace(scope, value, sourceState).ValueOrigins)
	}
	if _, raw := typeinfo.Underlying(typ).(*typeinfo.RawPtrType); raw {
		if origins, known := a.rawPointerOrigins(c, scope, value, sourceState); known {
			st.rawPointers = setOriginFact(st.rawPointers, storage, origins)
		}
	}
	if value == nil {
		return
	}
	if literal, ok := value.(*ast.StructLit); ok {
		strct, structured := typeinfo.Underlying(typ).(*typeinfo.StructType)
		if !structured || strct == nil {
			return
		}
		for _, fieldValue := range literal.Fields {
			if fieldValue.Name == nil {
				continue
			}
			field, _, found := typeinfo.LookupStructField(strct, fieldValue.Name.Name)
			if !found {
				continue
			}
			a.updateOriginPlace(c, scope, place.FieldOrigins(storage, field.Name), field.Type, fieldValue.Value, sourceState, st)
		}
		return
	}
	construction, constructed := a.module.Typechecking.VariantConstructions[value.ID()]
	if !constructed || construction.Payload == nil || construction.Case < 0 {
		source := c.resolveFlowPlace(scope, value, sourceState)
		a.copyStoredOriginPlace(storage, source.ValueOrigins, typ, sourceState, st)
		return
	}
	payloadStorage := place.VariantPayloadOrigins(storage, []int{construction.Case})
	a.updateOriginPlace(c, scope, payloadStorage, construction.Payload, construction.Value, sourceState, st)
}

func (a *flowAnalyzer) copyStoredOriginPlace(destination, source []place.Origin, typ typeinfo.Type, sourceState flowState, st *flowState) {
	if a == nil || st == nil || len(destination) == 0 || len(source) == 0 {
		return
	}
	if _, _, reference := typeinfo.ReferenceValueTarget(typ); reference {
		if value := originValues(sourceState.references, source); len(value) > 0 {
			st.references = setOriginFact(st.references, destination, value)
		}
		return
	}
	if _, raw := typeinfo.Underlying(typ).(*typeinfo.RawPtrType); raw {
		if value := originValues(sourceState.rawPointers, source); len(value) > 0 {
			st.rawPointers = setOriginFact(st.rawPointers, destination, value)
		}
		return
	}
	descriptor, variant := typeinfo.VariantDescriptorOf(typ)
	if !variant || descriptor.Family != typeinfo.VariantFamilyNamed {
		return
	}
	for caseIndex, variantCase := range descriptor.Cases {
		payload, payloadFound := typeinfo.Underlying(variantCase.Payload).(*typeinfo.StructType)
		if !payloadFound || payload == nil {
			continue
		}
		destinationPayload := place.VariantPayloadOrigins(destination, []int{caseIndex})
		sourcePayload := place.VariantPayloadOrigins(source, []int{caseIndex})
		for _, field := range payload.Fields {
			a.copyStoredOriginPlace(
				place.FieldOrigins(destinationPayload, field.Name),
				place.FieldOrigins(sourcePayload, field.Name),
				field.Type,
				sourceState,
				st,
			)
		}
	}
}

func (a *flowAnalyzer) invalidateCall(c *checker, scope *symbols.Scope, call *ast.CallExpr, st *flowState) {
	if call == nil || call.Callee == nil || st == nil {
		return
	}
	calleeType := a.result.ExprTypes[call.Callee.ID()]
	if calleeType == nil {
		calleeType = a.module.BaseExprType(call.Callee.ID())
	}
	fn, _ := typeinfo.Underlying(calleeType).(*typeinfo.FuncType)
	args := a.module.Typechecking.CallArgumentsOrSource(call)
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
			origins := originValues(st.rawPointers, []place.Origin{{Root: sym}})
			return origins, len(origins) > 0
		}
	case *ast.AsExpr:
		return a.rawPointerOrigins(c, scope, node.Expr, st)
	}
	origins := c.resolveFlowPlace(scope, expr, st).ValueOrigins
	return origins, len(origins) > 0
}

func (a *flowAnalyzer) applyConditionEdge(site *cfg.Site, edge cfg.EdgeKind, st *flowState, events *flowExpressionEvents) {
	if a == nil || a.graph == nil || site == nil || st == nil ||
		(edge != cfg.EdgeTrue && edge != cfg.EdgeFalse) {
		return
	}
	// CFG owns control topology. Once a source construct has become a Branch,
	// flow analysis must consume the branch's published condition identity
	// rather than rediscovering whether the source statement was an if or loop.
	if site.ID.Block < 0 || site.ID.Block >= len(a.graph.Blocks) {
		return
	}
	block := a.graph.Blocks[site.ID.Block]
	if block == nil {
		return
	}
	branch, ok := block.Terminator.(*cfg.Branch)
	if !ok || branch == nil || branch.ConditionID == 0 {
		return
	}
	condition, _ := a.module.TypedASTNodes[ast.NodeID(branch.ConditionID)].(ast.Expr)
	if condition == nil {
		return
	}
	scope := a.module.Bindings.BlockScopes[ast.NodeID(site.ScopeID)]
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
	if test, found := a.result.CaseTests[expr.ID()]; found {
		subject, _ := a.module.TypedASTNodes[test.SubjectID].(ast.Expr)
		checker := &checker{ctx: a.ctx, module: a.module, flow: &flowCheck{result: a.result, state: &st}}
		resolution := checker.resolveFlowPlace(scope, subject, st)
		if !resolution.Stable || len(resolution.StorageOrigins) == 0 {
			if test.Family == typeinfo.VariantFamilyOptional {
				a.ctx.Diagnostics.Add(unstableOptionalNarrowingError(subject))
			}
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

func provenVariantCase(facts []variantStateFact, origins []place.Origin, caseCount int) (int, bool) {
	fact, found := variantFact(facts, origins)
	if !found || fact.caseCount != caseCount || len(fact.cases) != 1 {
		return 0, false
	}
	return fact.cases[0], true
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
	merged.references = mergeOriginFacts(left.references, right.references)
	merged.rawPointers = mergeOriginFacts(left.rawPointers, right.rawPointers)
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
	if !sameOriginFacts(left.references, right.references) || !sameOriginFacts(left.rawPointers, right.rawPointers) {
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

func cloneOriginFacts(facts []originStateFact) []originStateFact {
	cloned := make([]originStateFact, len(facts))
	for index, fact := range facts {
		cloned[index] = originStateFact{
			storage: place.CloneOrigins(fact.storage), value: place.CloneOrigins(fact.value),
		}
	}
	return cloned
}

func originValues(facts []originStateFact, storage []place.Origin) []place.Origin {
	for _, fact := range facts {
		if place.SameOrigins(fact.storage, storage) {
			return place.CloneOrigins(fact.value)
		}
	}
	return nil
}

func setOriginFact(facts []originStateFact, storage, value []place.Origin) []originStateFact {
	if len(storage) == 0 || len(value) == 0 {
		return facts
	}
	for index := range facts {
		if place.SameOrigins(facts[index].storage, storage) {
			facts[index].value = place.CloneOrigins(value)
			return facts
		}
	}
	return append(facts, originStateFact{storage: place.CloneOrigins(storage), value: place.CloneOrigins(value)})
}

func mergeOriginFacts(left, right []originStateFact) []originStateFact {
	merged := make([]originStateFact, 0, min(len(left), len(right)))
	for _, leftFact := range left {
		rightValue := originValues(right, leftFact.storage)
		if len(rightValue) == 0 {
			continue
		}
		merged = append(merged, originStateFact{
			storage: place.CloneOrigins(leftFact.storage),
			value:   place.MergeOrigins(leftFact.value, rightValue),
		})
	}
	return merged
}

func sameOriginFacts(left, right []originStateFact) bool {
	if len(left) != len(right) {
		return false
	}
	for _, fact := range left {
		if !place.SameOrigins(fact.value, originValues(right, fact.storage)) {
			return false
		}
	}
	return true
}

func invalidateOriginFacts(facts []originStateFact, mutated []place.Origin) []originStateFact {
	kept := facts[:0]
	for _, fact := range facts {
		if !place.OriginsOverlap(fact.storage, mutated) {
			kept = append(kept, fact)
		}
	}
	return kept
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

func invalidateVariantOrigins(st *flowState, mutated []place.Origin) {
	if st == nil || len(mutated) == 0 {
		return
	}
	kept := st.variants[:0]
	for _, fact := range st.variants {
		dependencyMutated := false
		for _, dependency := range fact.dependencies {
			if dependency != nil && place.OriginsOverlap([]place.Origin{{Root: dependency}}, mutated) {
				dependencyMutated = true
				break
			}
		}
		if dependencyMutated {
			continue
		}
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
		root := []place.Origin{{Root: sym}}
		st.references = clearOriginRoot(st.references, root)
		st.rawPointers = clearOriginRoot(st.rawPointers, root)
		invalidateVariantOrigins(st, root)
	}
}

func clearOriginRoot(facts []originStateFact, root []place.Origin) []originStateFact {
	kept := facts[:0]
	for _, fact := range facts {
		if !place.OriginsOverlap(fact.storage, root) && !place.OriginsOverlap(fact.value, root) {
			kept = append(kept, fact)
		}
	}
	return kept
}
