package typechecker

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	graphcore "compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/project"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"compiler/pkg/typednil"
)

type flowDemand uint8

const (
	flowDefault flowDemand = iota
	flowPayload
	flowCarrier
	flowOptionalTest
)

type flowCallEvent struct {
	order int
	call  *thir.Call
}

type flowEvents struct {
	next  int
	tests map[ir.NodeID]int
	calls []flowCallEvent
}

// flowAnalyzer owns path-sensitive semantics over immutable THIR and CFG.
// Base typing has already resolved names, types, calls, places, and control
// evidence; this phase only refines those facts along control-flow edges.
type flowAnalyzer struct {
	ctx           *project.CompilerContext
	module        *module.Module
	source        *thir.Module
	function      *thir.Function
	functionScope *symbols.Scope
	graph         *cfg.ControlFlowGraph
	result        *flowresult.Result
	inStates      map[cfg.SiteID]flowState
	bindingTypes  map[*symbols.Symbol]typeinfo.Type

	state    *flowState
	events   *flowEvents
	expected typeinfo.Type
	demand   flowDemand
}

func CheckFlow(ctx *project.CompilerContext, mod *module.Module) *flowresult.Result {
	result := flowresult.New()
	if ctx == nil || mod == nil || mod.THIR == nil || mod.CFG == nil {
		return result
	}
	for _, graph := range mod.CFG.Functions {
		if graph == nil {
			continue
		}
		function := mod.THIR.Function(graph.NodeID)
		if function == nil || function.Symbol == nil || function.Symbol.Scope == nil {
			continue
		}
		analyzer := &flowAnalyzer{
			ctx: ctx, module: mod, source: mod.THIR, function: function,
			functionScope: function.Symbol.Scope, graph: graph, result: result,
			inStates: make(map[cfg.SiteID]flowState), bindingTypes: make(map[*symbols.Symbol]typeinfo.Type),
		}
		analyzer.run()
	}
	return result
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
				order = append(order, site.ID)
				disconnected[site.ID] = !block.IsReachable
			}
		}
	}
	entryState := newFlowState()
	for _, sym := range a.functionScope.Symbols() {
		if sym == nil || sym.Kind != symbols.SymbolParam {
			continue
		}
		typ, ok := symbols.GetSymbolType(sym)
		if !ok {
			continue
		}
		if _, _, isReference := typeinfo.ReferenceValueTarget(typ); isReference {
			carrier := []place.Origin{{Root: sym}}
			cases := make([]int, optionalLayerCount(typ))
			for index := range cases {
				cases[index] = ir.OptionalPresentCase
			}
			entryState.references = setOriginFact(entryState.references, carrier, place.VariantPayloadOrigins(carrier, cases))
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
			for _, candidate := range order {
				if _, visited := a.inStates[candidate]; visited || !disconnected[candidate] {
					continue
				}
				a.inStates[candidate] = copyFlowState(entryState)
				work.Add(candidate)
				seeded = true
				break
			}
			if !seeded {
				break
			}
			continue
		}
		site := a.graph.Site(id)
		if site == nil {
			continue
		}
		next := copyFlowState(a.inStates[id])
		events := a.applySite(site, &next)
		for _, edge := range a.graph.SiteEdges.OutEdges(site.ID) {
			if a.graph.Site(edge.To) == nil {
				continue
			}
			out := copyFlowState(next)
			if site.Kind == cfg.SiteTerminator {
				a.applyConditionEdge(site, edge.Kind, &out, events)
				a.applyVariantCaseEdge(site, edge, &out)
			}
			if !out.isReachable {
				continue
			}
			current, exists := a.inStates[edge.To]
			merged := out
			if exists {
				merged = mergeFlowStates(current, out)
			}
			if exists && flowStatesEqual(current, merged) {
				continue
			}
			a.inStates[edge.To] = merged
			work.Add(edge.To)
		}
	}
}

func (a *flowAnalyzer) applySite(site *cfg.Site, state *flowState) *flowEvents {
	events := &flowEvents{tests: make(map[ir.NodeID]int)}
	if site == nil || state == nil {
		return events
	}
	a.state, a.events = state, events
	defer func() { a.state, a.events = nil, nil }()
	switch site.Kind {
	case cfg.SiteStatement, cfg.SiteTerminator:
		if statement, ok := a.source.Node(site.NodeID).(thir.Stmt); ok && !typednil.IsNil(statement) {
			statement.AnalyzeFlow(a)
		}
	case cfg.SiteScopeExit:
		if block, ok := a.source.Node(site.NodeID).(*thir.Block); ok && block != nil {
			clearFlowScope(block.Scope, state)
		}
	}
	return events
}

func (a *flowAnalyzer) analyze(expr thir.Expr, expected typeinfo.Type, demand flowDemand) typeinfo.Type {
	if typednil.IsNil(expr) {
		return nil
	}
	a.result.ForgetPayload(ast.NodeID(expr.SourceInfo().NodeID))
	previousExpected, previousDemand := a.expected, a.demand
	a.expected, a.demand = expected, demand
	typ := expr.AnalyzeFlow(a)
	a.expected, a.demand = previousExpected, previousDemand
	return typ
}

func (a *flowAnalyzer) finish(expr thir.Expr, base typeinfo.Type) typeinfo.Type {
	if typednil.IsNil(expr) || base == nil {
		return base
	}
	id := ast.NodeID(expr.SourceInfo().NodeID)
	resolution := a.resolve(expr, *a.state)
	if a.demand == flowCarrier {
		a.recordResolution(expr, resolution)
		return base
	}
	if !isOptionalType(base) {
		a.recordResolution(expr, resolution)
		return base
	}
	_, explicitCarrier := typeinfo.Underlying(a.expected).(*typeinfo.OptionalType)
	required := payloadDepthForExpected(base, a.expected)
	if a.demand == flowPayload && required == 0 && !explicitCarrier {
		required = optionalLayerCount(base)
	}
	payloadCases := provenOptionalPayloadCases(a.state.variants, resolution.StorageOrigins)
	resolved := unwrapOptionalLayers(base, len(payloadCases))
	applied := optionalLayerCount(base) - optionalLayerCount(resolved)
	payloadCases = payloadCases[:applied]
	if a.demand != flowOptionalTest && explicitCarrier {
		a.recordResolution(expr, resolution)
		return base
	}
	if applied > 0 {
		a.recordPayload(expr, resolution, payloadCases)
	}
	if _, _, isReference := typeinfo.ReferenceValueTarget(resolved); isReference {
		resolution.ValueOrigins = place.CloneOrigins(resolution.ValueOrigins)
	} else if _, isRaw := typeinfo.Underlying(resolved).(*typeinfo.RawPtrType); !isRaw {
		resolution.ValueOrigins = place.VariantPayloadOrigins(resolution.StorageOrigins, payloadCases)
	}
	a.recordResolution(expr, resolution)
	if a.demand != flowOptionalTest && required > applied {
		if expr.ExprPlace() != nil && !resolution.IsStable {
			a.ctx.Diagnostics.Add(unstableOptionalNarrowingAt(expr.SourceInfo().Location))
		} else {
			a.ctx.Diagnostics.Add(optionalPayloadProofAt(expr.SourceInfo().Location))
		}
		resolved = unwrapOptionalLayers(base, required)
	}
	a.result.RecordExprType(id, resolved)
	return resolved
}

func optionalPayloadProofAt(location *source.Location) *diagnostics.Diagnostic {
	return diagnostics.NewError("optional payload use requires a presence proof").
		WithPrimaryLabel(location, "payload is not proven present here").
		WithCode(diagnostics.ErrOptionalPayloadProof).
		WithHelp("guard this stable place with `value != none` or return after `value == none`")
}

func unstableOptionalNarrowingAt(location *source.Location) *diagnostics.Diagnostic {
	return diagnostics.NewError("optional narrowing subject is not a stable place").
		WithPrimaryLabel(location, "this expression can change between the test and use").
		WithCode(diagnostics.ErrUnstableNarrowing).
		WithHelp("bind the expression or index to a direct local before testing it")
}

func (a *flowAnalyzer) AnalyzeBlock(*thir.Block)             {}
func (a *flowAnalyzer) AnalyzeBreak(*thir.Break)             {}
func (a *flowAnalyzer) AnalyzeContinue(*thir.Continue)       {}
func (a *flowAnalyzer) AnalyzeInvalidStmt(*thir.InvalidStmt) {}

func (a *flowAnalyzer) AnalyzeBinding(statement *thir.Binding) {
	typ, _ := symbols.GetSymbolType(statement.Symbol)
	expected := typ
	if statement.IsInferred {
		expected = nil
	}
	valueType := a.analyze(statement.Value, expected, flowDefault)
	if statement.IsInferred && valueType != nil {
		typ = valueType
		a.bindingTypes[statement.Symbol] = valueType
	}
	if statement.Symbol != nil {
		a.updateOriginPlace([]place.Origin{{Root: statement.Symbol}}, typ, statement.Value, copyFlowState(*a.state), a.state)
	}
}

func (a *flowAnalyzer) AnalyzeExprStmt(statement *thir.ExprStmt) {
	a.analyze(statement.Value, nil, flowDefault)
}

func (a *flowAnalyzer) AnalyzeAssign(statement *thir.Assign) {
	a.analyze(statement.Target, nil, flowCarrier)
	sourceState := copyFlowState(*a.state)
	resolution := a.resolve(statement.Target, *a.state)
	invalidateVariantOrigins(a.state, resolution.StorageOrigins)
	a.analyze(statement.Value, statement.Target.ExprType(), flowDefault)
	a.updateOriginPlace(resolution.StorageOrigins, statement.Target.ExprType(), statement.Value, sourceState, a.state)
}

func (a *flowAnalyzer) AnalyzeReturn(statement *thir.Return) {
	a.analyze(statement.Value, a.function.ReturnType, flowDefault)
}

func (a *flowAnalyzer) AnalyzeIf(statement *thir.If) {
	a.analyze(statement.Condition, nil, flowDefault)
}

func (a *flowAnalyzer) AnalyzeFor(statement *thir.For) {
	if statement.Checked != nil {
		return
	}
	a.analyze(statement.Iterable, nil, flowDefault)
	a.analyze(statement.Condition, nil, flowDefault)
}

func (a *flowAnalyzer) AnalyzeMatch(statement *thir.Match) {
	a.analyze(statement.Subject, nil, flowCarrier)
}

func (a *flowAnalyzer) AnalyzeInvalidExpr(expr *thir.InvalidExpr) typeinfo.Type {
	return a.finish(expr, expr.ExprType())
}
func (a *flowAnalyzer) AnalyzeNumberLiteral(expr *thir.NumberLiteral) typeinfo.Type {
	return a.finish(expr, expr.ExprType())
}
func (a *flowAnalyzer) AnalyzeStringLiteral(expr *thir.StringLiteral) typeinfo.Type {
	return a.finish(expr, expr.ExprType())
}
func (a *flowAnalyzer) AnalyzeByteLiteral(expr *thir.ByteLiteral) typeinfo.Type {
	return a.finish(expr, expr.ExprType())
}
func (a *flowAnalyzer) AnalyzeCharLiteral(expr *thir.CharLiteral) typeinfo.Type {
	return a.finish(expr, expr.ExprType())
}
func (a *flowAnalyzer) AnalyzeBoolLiteral(expr *thir.BoolLiteral) typeinfo.Type {
	return a.finish(expr, expr.ExprType())
}
func (a *flowAnalyzer) AnalyzeNoneLiteral(expr *thir.NoneLiteral) typeinfo.Type {
	return a.finish(expr, expr.ExprType())
}
func (a *flowAnalyzer) AnalyzeIdent(expr *thir.Ident) typeinfo.Type {
	typ, refined := a.bindingTypes[expr.Symbol]
	if typ == nil {
		typ, _ = symbols.GetSymbolType(expr.Symbol)
	}
	if typ == nil {
		typ = expr.ExprType()
	}
	resolved := a.finish(expr, typ)
	if refined {
		a.result.RecordExprType(ast.NodeID(expr.SourceInfo().NodeID), resolved)
	}
	return resolved
}
func (a *flowAnalyzer) AnalyzeQualifiedIdent(expr *thir.QualifiedIdent) typeinfo.Type {
	typ, _ := symbols.GetSymbolType(expr.Symbol)
	if typ == nil {
		typ = expr.ExprType()
	}
	return a.finish(expr, typ)
}

func (a *flowAnalyzer) AnalyzeField(expr *thir.Field) typeinfo.Type {
	a.analyze(expr.Base, nil, flowPayload)
	baseType := expr.Base.ExprType()
	if ident, ok := expr.Base.(*thir.Ident); ok && ident.Symbol != nil {
		baseType, _ = symbols.GetSymbolType(ident.Symbol)
	}
	descriptor, isVariant := typeinfo.VariantDescriptorOf(baseType)
	if !isVariant || descriptor.Family != typeinfo.VariantFamilyNamed {
		return a.finish(expr, expr.ExprType())
	}
	resolution := a.resolve(expr.Base, *a.state)
	caseIndex, exact := provenVariantCase(a.state.variants, resolution.StorageOrigins, len(descriptor.Cases))
	if exact {
		payload, _ := typeinfo.Underlying(descriptor.Cases[caseIndex].Payload).(*typeinfo.StructType)
		if field, fieldIndex, found := typeinfo.LookupStructField(payload, expr.Name); found {
			a.recordPayload(expr.Base, resolution, []int{caseIndex})
			a.result.RecordPayload(ast.NodeID(expr.Source.NodeID), flowresult.PayloadAccess{
				CarrierOrigins: place.CloneOrigins(resolution.StorageOrigins), Cases: []int{caseIndex},
			})
			a.result.RecordVariantField(ast.NodeID(expr.Source.NodeID), flowresult.VariantFieldAccess{
				Carrier: ast.NodeID(expr.Base.SourceInfo().NodeID), Case: caseIndex,
				Payload: payload, Field: fieldIndex, Type: field.Type,
			})
			a.result.RecordExprType(ast.NodeID(expr.Source.NodeID), field.Type)
			return a.finish(expr, field.Type)
		}
	}
	a.ctx.Diagnostics.AddError(diagnostics.ErrFieldNotFound,
		"unknown member `"+expr.Name+"`", expr.Source.Location, "")
	return a.finish(expr, &typeinfo.InvalidType{})
}

func (a *flowAnalyzer) AnalyzeIndex(expr *thir.Index) typeinfo.Type {
	a.analyze(expr.Base, nil, flowPayload)
	a.analyze(expr.Index, typeinfo.DefaultIntegerType(), flowDefault)
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeRange(expr *thir.Range) typeinfo.Type {
	if !typednil.IsNil(expr.Start) {
		a.analyze(expr.Start, expr.Start.ExprType(), flowDefault)
	}
	if !typednil.IsNil(expr.End) {
		a.analyze(expr.End, expr.End.ExprType(), flowDefault)
	}
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeStructLiteral(expr *thir.StructLiteral) typeinfo.Type {
	semantic, _ := typeinfo.Underlying(expr.ExprType()).(*typeinfo.StructType)
	for _, field := range expr.Fields {
		var expected typeinfo.Type
		if semantic != nil && field.Index >= 0 && field.Index < len(semantic.Fields) {
			expected = semantic.Fields[field.Index].Type
		}
		a.analyze(field.Value, expected, flowDefault)
	}
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeVariant(expr *thir.Variant) typeinfo.Type {
	var expected typeinfo.Type
	if descriptor, ok := typeinfo.VariantDescriptorOf(expr.ExprType()); ok && expr.Case >= 0 && expr.Case < len(descriptor.Cases) {
		expected = descriptor.Cases[expr.Case].Payload
	}
	a.analyze(expr.Payload, expected, flowDefault)
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeArrayLiteral(expr *thir.ArrayLiteral) typeinfo.Type {
	var expected typeinfo.Type
	if array, ok := typeinfo.Underlying(expr.ExprType()).(*typeinfo.ArrayType); ok {
		expected = array.Elem
	}
	for _, value := range expr.Values {
		a.analyze(value, expected, flowDefault)
	}
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeAddress(expr *thir.Address) typeinfo.Type {
	demand := flowDefault
	var expected typeinfo.Type
	if expr.Mode == thir.AddressRaw {
		demand = flowCarrier
	} else if target, _, isReference := typeinfo.ReferenceValueTarget(typeinfo.Underlying(a.expected)); isReference {
		expected = target
	}
	a.analyze(expr.Value, expected, demand)
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeUnary(expr *thir.Unary) typeinfo.Type {
	a.analyze(expr.Value, nil, flowPayload)
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeBinary(expr *thir.Binary) typeinfo.Type {
	demand := flowPayload
	if expr.Test != nil {
		demand = flowOptionalTest
	}
	a.analyze(expr.Left, nil, demand)
	a.analyze(expr.Right, nil, demand)
	if expr.Test != nil {
		a.recordCaseTest(expr, expr.Test)
	}
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeIs(expr *thir.Is) typeinfo.Type {
	a.analyze(expr.Value, nil, flowCarrier)
	if expr.Test != nil {
		a.recordCaseTest(expr, expr.Test)
	}
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeCall(expr *thir.Call) typeinfo.Type {
	calleeType := a.analyze(expr.Callee, nil, flowPayload)
	fn, _ := typeinfo.Underlying(calleeType).(*typeinfo.FuncType)
	offset := 0
	if _, isMethod := expr.Callee.(*thir.Field); isMethod {
		offset = 1
	}
	for index, argument := range expr.Args {
		var expected typeinfo.Type
		if fn != nil && index+offset < len(fn.Params) {
			expected = fn.Params[index+offset]
		}
		a.analyze(argument, expected, flowDefault)
	}
	a.invalidateCall(expr, a.state)
	if a.events != nil {
		a.events.next++
		a.events.calls = append(a.events.calls, flowCallEvent{order: a.events.next, call: expr})
	}
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeFree(expr *thir.Free) typeinfo.Type {
	a.analyze(expr.Value, nil, flowPayload)
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzePrint(expr *thir.Print) typeinfo.Type {
	a.analyze(expr.Value, nil, flowPayload)
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) AnalyzeCast(expr *thir.Cast) typeinfo.Type {
	a.analyze(expr.Value, nil, flowPayload)
	return a.finish(expr, expr.ExprType())
}

func (a *flowAnalyzer) recordCaseTest(expr thir.Expr, test *thir.CaseTest) {
	if test == nil {
		return
	}
	refined := flowresult.CaseTest{
		SubjectID: ast.NodeID(test.SubjectID), Case: test.Case, MatchesWhenTrue: test.MatchesWhenTrue,
		CaseCount: test.CaseCount, Family: test.Family,
	}
	if payload, ok := a.result.Payload(ast.NodeID(test.SubjectID)); ok {
		storage := a.result.StorageOrigins(ast.NodeID(test.SubjectID))
		if payload.AppliesTo(storage) {
			refined.PayloadPath = append([]int(nil), payload.Cases...)
		}
	}
	id := expr.SourceInfo().NodeID
	a.result.RecordCaseTest(ast.NodeID(id), refined)
	if a.events != nil {
		a.events.next++
		a.events.tests[id] = a.events.next
	}
}

func (a *flowAnalyzer) recordPayload(expr thir.Expr, resolution place.Resolution, cases []int) {
	if typednil.IsNil(expr) || len(cases) == 0 {
		return
	}
	isDirect := len(resolution.StorageOrigins) == 1 && resolution.StorageOrigins[0].Root != nil && len(resolution.StorageOrigins[0].Projections) == 0
	a.result.RecordPayload(ast.NodeID(expr.SourceInfo().NodeID), flowresult.PayloadAccess{
		CarrierOrigins: place.CloneOrigins(resolution.StorageOrigins), Cases: append([]int(nil), cases...), IsDirect: isDirect,
	})
}

func (a *flowAnalyzer) recordResolution(expr thir.Expr, resolution place.Resolution) {
	a.result.RecordOrigins(ast.NodeID(expr.SourceInfo().NodeID), resolution.StorageOrigins, resolution.ValueOrigins)
}

func (a *flowAnalyzer) expressionType(expr thir.Expr) typeinfo.Type {
	if typednil.IsNil(expr) {
		return nil
	}
	if typ := a.result.ExprType(ast.NodeID(expr.SourceInfo().NodeID)); typ != nil {
		return typ
	}
	return expr.ExprType()
}

func (a *flowAnalyzer) resolve(expr thir.Expr, state flowState) place.Resolution {
	if typednil.IsNil(expr) {
		return place.Resolution{}
	}
	switch node := expr.(type) {
	case *thir.Address:
		resolved := a.resolve(node.Value, state)
		if payload, ok := a.result.Payload(ast.NodeID(node.Value.SourceInfo().NodeID)); ok {
			resolved.ValueOrigins = place.VariantPayloadOrigins(resolved.StorageOrigins, payload.Cases)
		}
		return resolved
	case *thir.Ident:
		return a.resolveSymbol(node.Symbol, node.ExprType(), state)
	case *thir.QualifiedIdent:
		return a.resolveSymbol(node.Symbol, node.ExprType(), state)
	case *thir.Field:
		base := a.resolve(node.Base, state)
		origins := a.projectedBaseOrigins(base, node.Base)
		origins = appendIndirectOrigins(origins, a.expressionType(node.Base))
		origins = place.FieldOrigins(origins, node.Name)
		return a.resolveStored(node.ExprType(), place.Resolution{
			StorageOrigins: origins, ValueOrigins: place.CloneOrigins(origins),
			Dependencies: append([]*symbols.Symbol(nil), base.Dependencies...), IsStable: base.IsStable && len(origins) > 0,
		}, state)
	case *thir.Index:
		base := a.resolve(node.Base, state)
		origins := appendIndirectOrigins(a.projectedBaseOrigins(base, node.Base), a.expressionType(node.Base))
		dependencies := append([]*symbols.Symbol(nil), base.Dependencies...)
		isStable := base.IsStable
		switch {
		case node.Constant != nil:
			origins = appendOrigin(origins, place.OriginProjection{Kind: place.OriginIndex, Index: node.Constant.Text})
		case isTHIRRange(node.Index):
			origins = appendOrigin(origins, place.OriginProjection{Kind: place.OriginWildcard})
			return place.Resolution{
				StorageOrigins: origins,
				ValueOrigins:   place.CloneOrigins(origins),
				Dependencies:   dependencies,
			}
		case integralBinding(node.Index) != nil:
			binding := integralBinding(node.Index)
			origins = appendOrigin(origins, place.OriginProjection{Kind: place.OriginBindingIndex, Binding: binding})
			dependencies = append(dependencies, binding)
		default:
			origins = appendOrigin(origins, place.OriginProjection{Kind: place.OriginWildcard})
			isStable = false
		}
		return a.resolveStored(node.ExprType(), place.Resolution{
			StorageOrigins: origins, ValueOrigins: place.CloneOrigins(origins),
			Dependencies: dependencies, IsStable: isStable && len(origins) > 0,
		}, state)
	case *thir.Call:
		return place.Resolution{ValueOrigins: a.callOrigins(node, state)}
	default:
		return place.Resolution{}
	}
}

func (a *flowAnalyzer) resolveSymbol(symbol *symbols.Symbol, typ typeinfo.Type, state flowState) place.Resolution {
	if symbol == nil {
		return place.Resolution{}
	}
	if inferred := a.bindingTypes[symbol]; inferred != nil {
		typ = inferred
	}
	origins := []place.Origin{{Root: symbol}}
	return a.resolveStored(typ, place.Resolution{StorageOrigins: origins, ValueOrigins: place.CloneOrigins(origins), IsStable: true}, state)
}

func (a *flowAnalyzer) resolveStored(typ typeinfo.Type, resolution place.Resolution, state flowState) place.Resolution {
	if _, _, isReference := typeinfo.ReferenceValueTarget(typ); isReference {
		resolution.ValueOrigins = place.CloneOrigins(originValues(state.references, resolution.StorageOrigins))
	} else if _, isRaw := typeinfo.Underlying(typ).(*typeinfo.RawPtrType); isRaw {
		resolution.ValueOrigins = place.CloneOrigins(originValues(state.rawPointers, resolution.StorageOrigins))
	}
	return resolution
}

func (a *flowAnalyzer) projectedBaseOrigins(base place.Resolution, expr thir.Expr) []place.Origin {
	origins := base.ValueOrigins
	if _, _, isReference := typeinfo.ReferenceTarget(typeinfo.Underlying(a.expressionType(expr))); isReference {
		return origins
	}
	if payload, ok := a.result.Payload(ast.NodeID(expr.SourceInfo().NodeID)); ok {
		origins = place.VariantPayloadOrigins(origins, payload.Cases)
	}
	return origins
}

func appendIndirectOrigins(origins []place.Origin, typ typeinfo.Type) []place.Origin {
	if _, isOwned := typeinfo.PointerTarget(typeinfo.Underlying(typ)); !isOwned {
		return origins
	}
	return appendOrigin(origins, place.OriginProjection{Kind: place.OriginPointee})
}

func appendOrigin(origins []place.Origin, projection place.OriginProjection) []place.Origin {
	out := place.CloneOrigins(origins)
	for index := range out {
		path := out[index].Projections
		if len(path) == 0 || path[len(path)-1].Kind != place.OriginWildcard {
			out[index].Projections = append(path, projection)
		}
	}
	return out
}

func isTHIRRange(expr thir.Expr) bool {
	_, ok := expr.(*thir.Range)
	return ok
}

func integralBinding(expr thir.Expr) *symbols.Symbol {
	ident, ok := expr.(*thir.Ident)
	if !ok || ident.Symbol == nil {
		return nil
	}
	typ, typed := symbols.GetSymbolType(ident.Symbol)
	if !typed || !typeinfo.IsIntegral(typ) {
		return nil
	}
	return ident.Symbol
}

func (a *flowAnalyzer) callOrigins(call *thir.Call, state flowState) []place.Origin {
	if call == nil || typednil.IsNil(call.Callee) {
		return nil
	}
	fn, _ := typeinfo.Underlying(a.expressionType(call.Callee)).(*typeinfo.FuncType)
	if fn == nil || fn.ReturnOrigins == nil {
		return nil
	}
	field, isMethod := call.Callee.(*thir.Field)
	var origins []place.Origin
	for _, slot := range fn.ReturnOrigins.Sources {
		var source thir.Expr
		if isMethod {
			if slot == 0 {
				source = field.Base
			} else if slot > 0 && slot <= len(call.Args) {
				source = call.Args[slot-1]
			}
		} else if slot >= 0 && slot < len(call.Args) {
			source = call.Args[slot]
		}
		if !typednil.IsNil(source) {
			origins = place.MergeOrigins(origins, a.resolve(source, state).ValueOrigins)
		}
	}
	return origins
}

func (a *flowAnalyzer) updateOriginPlace(storage []place.Origin, typ typeinfo.Type, value thir.Expr, sourceState flowState, state *flowState) {
	if state == nil || len(storage) == 0 {
		return
	}
	state.references = invalidateOriginFacts(state.references, storage)
	state.rawPointers = invalidateOriginFacts(state.rawPointers, storage)
	if _, _, isReference := typeinfo.ReferenceValueTarget(typ); isReference {
		state.references = setOriginFact(state.references, storage, a.resolve(value, sourceState).ValueOrigins)
	}
	if _, isRawPointer := typeinfo.Underlying(typ).(*typeinfo.RawPtrType); isRawPointer {
		if origins, isKnown := a.rawPointerOrigins(value, sourceState); isKnown {
			state.rawPointers = setOriginFact(state.rawPointers, storage, origins)
		}
	}
	if typednil.IsNil(value) {
		return
	}
	switch expression := value.(type) {
	case *thir.StructLiteral:
		slots := make([]flowresult.AggregateSlot, 0, len(expression.Fields))
		semantic, _ := typeinfo.Underlying(typ).(*typeinfo.StructType)
		for _, field := range expression.Fields {
			if typednil.IsNil(field.Value) || semantic == nil || field.Index < 0 || field.Index >= len(semantic.Fields) {
				continue
			}
			semanticField := semantic.Fields[field.Index]
			slots = append(slots, flowresult.AggregateSlot{
				ValueExpr:  field.Value,
				Projection: place.OriginProjection{Kind: place.OriginField, Field: semanticField.Name},
			})
			a.updateOriginPlace(place.FieldOrigins(storage, semanticField.Name), semanticField.Type, field.Value, sourceState, state)
		}
		a.result.RecordAggregateSlots(ast.NodeID(expression.Source.NodeID), slots)
	case *thir.Variant:
		if typednil.IsNil(expression.Payload) || expression.Case < 0 {
			return
		}
		a.result.RecordAggregateSlots(ast.NodeID(expression.Source.NodeID), []flowresult.AggregateSlot{{
			ValueExpr:  expression.Payload,
			Projection: place.OriginProjection{Kind: place.OriginVariantPayload, Case: expression.Case},
		}})
		var payloadType typeinfo.Type
		if descriptor, ok := typeinfo.VariantDescriptorOf(typ); ok && expression.Case < len(descriptor.Cases) {
			payloadType = descriptor.Cases[expression.Case].Payload
		}
		a.updateOriginPlace(place.VariantPayloadOrigins(storage, []int{expression.Case}), payloadType, expression.Payload, sourceState, state)
	default:
		source := a.resolve(value, sourceState)
		a.copyStoredOriginPlace(storage, source.ValueOrigins, typ, sourceState, state)
	}
}

func (a *flowAnalyzer) copyStoredOriginPlace(destination, source []place.Origin, typ typeinfo.Type, sourceState flowState, state *flowState) {
	if state == nil || len(destination) == 0 || len(source) == 0 {
		return
	}
	if _, _, isReference := typeinfo.ReferenceValueTarget(typ); isReference {
		if value := originValues(sourceState.references, source); len(value) > 0 {
			state.references = setOriginFact(state.references, destination, value)
		}
		return
	}
	if _, isRaw := typeinfo.Underlying(typ).(*typeinfo.RawPtrType); isRaw {
		if value := originValues(sourceState.rawPointers, source); len(value) > 0 {
			state.rawPointers = setOriginFact(state.rawPointers, destination, value)
		}
		return
	}
	descriptor, isVariant := typeinfo.VariantDescriptorOf(typ)
	if !isVariant || descriptor.Family != typeinfo.VariantFamilyNamed {
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
			a.copyStoredOriginPlace(place.FieldOrigins(destinationPayload, field.Name), place.FieldOrigins(sourcePayload, field.Name), field.Type, sourceState, state)
		}
	}
}

func (a *flowAnalyzer) rawPointerOrigins(expr thir.Expr, state flowState) ([]place.Origin, bool) {
	switch node := expr.(type) {
	case *thir.Address:
		if node.Mode == thir.AddressRaw {
			origins := a.resolve(node.Value, state).ValueOrigins
			return origins, len(origins) > 0
		}
	case *thir.Ident:
		origins := originValues(state.rawPointers, []place.Origin{{Root: node.Symbol}})
		return origins, len(origins) > 0
	case *thir.Cast:
		return a.rawPointerOrigins(node.Value, state)
	}
	origins := a.resolve(expr, state).ValueOrigins
	return origins, len(origins) > 0
}

func (a *flowAnalyzer) invalidateCall(call *thir.Call, state *flowState) {
	if call == nil || typednil.IsNil(call.Callee) || state == nil {
		return
	}
	fn, _ := typeinfo.Underlying(a.expressionType(call.Callee)).(*typeinfo.FuncType)
	args := append([]thir.Expr(nil), call.Args...)
	if field, isMethod := call.Callee.(*thir.Field); isMethod {
		args = append([]thir.Expr{field.Base}, args...)
	}
	if fn != nil && len(fn.Params) == len(args) {
		for index, argument := range args {
			param := fn.Params[index]
			if _, isMutable, isReference := typeinfo.ReferenceValueTarget(param); isReference && isMutable {
				invalidateVariantOrigins(state, a.resolve(argument, *state).ValueOrigins)
			}
			if _, isRaw := typeinfo.Underlying(param).(*typeinfo.RawPtrType); isRaw {
				if origins, isKnown := a.rawPointerOrigins(argument, *state); isKnown {
					invalidateVariantOrigins(state, origins)
				} else {
					state.variants = nil
				}
			}
		}
	}
	if a.module.ModuleScope != nil {
		for _, symbol := range a.module.ModuleScope.Symbols() {
			if symbol != nil && symbol.IsMutable() {
				invalidateVariantOrigins(state, []place.Origin{{Root: symbol}})
			}
		}
	}
}

func (a *flowAnalyzer) applyVariantCaseEdge(site *cfg.Site, edge cfg.Edge, state *flowState) {
	if site == nil || state == nil || edge.Kind != cfg.EdgeVariantCase {
		return
	}
	match, _ := a.source.Node(site.NodeID).(*thir.Match)
	if match == nil || typednil.IsNil(match.Subject) {
		return
	}
	resolution := a.resolve(match.Subject, *state)
	if resolution.IsStable && len(resolution.StorageOrigins) > 0 {
		restrictVariantFact(state, variantStateFact{
			origins: resolution.StorageOrigins, cases: []int{edge.Case}, caseCount: match.CaseCount,
			dependencies: append([]*symbols.Symbol(nil), resolution.Dependencies...),
		})
	}
	var selected *thir.MatchArm
	for index := range match.Arms {
		if match.Arms[index].Case == edge.Case {
			selected = &match.Arms[index]
			break
		}
	}
	if selected == nil || selected.Payload == nil {
		return
	}
	payloadOrigins := place.VariantPayloadOrigins(resolution.ValueOrigins, []int{edge.Case})
	for _, binding := range selected.Bindings {
		fieldOrigins := payloadOrigins
		switch binding.Projection {
		case thir.MatchPayloadField:
			payload, ok := typeinfo.Underlying(selected.Payload).(*typeinfo.StructType)
			if !ok || binding.Field < 0 || binding.Field >= len(payload.Fields) {
				continue
			}
			fieldOrigins = place.FieldOrigins(payloadOrigins, payload.Fields[binding.Field].Name)
		case thir.MatchWholePayload:
		default:
			panic("flow typing: invalid match binding projection")
		}
		if binding.Symbol == nil {
			continue
		}
		storage := []place.Origin{{Root: binding.Symbol}}
		valueOrigins := fieldOrigins
		if _, _, isReference := typeinfo.ReferenceValueTarget(binding.Type); isReference {
			valueOrigins = originValues(state.references, fieldOrigins)
			if len(valueOrigins) == 0 {
				valueOrigins = fieldOrigins
			}
			state.references = setOriginFact(state.references, storage, valueOrigins)
		}
		if _, isRaw := typeinfo.Underlying(binding.Type).(*typeinfo.RawPtrType); isRaw {
			valueOrigins = originValues(state.rawPointers, fieldOrigins)
			if len(valueOrigins) == 0 {
				valueOrigins = fieldOrigins
			}
			state.rawPointers = setOriginFact(state.rawPointers, storage, valueOrigins)
		}
		if binding.Source.NodeID != 0 {
			a.result.MergeOrigins(ast.NodeID(binding.Source.NodeID), storage, valueOrigins)
		}
	}
}

func (a *flowAnalyzer) applyConditionEdge(site *cfg.Site, edge cfg.EdgeKind, state *flowState, events *flowEvents) {
	if site == nil || state == nil || edge != cfg.EdgeTrue && edge != cfg.EdgeFalse || site.ID.Block < 0 || site.ID.Block >= len(a.graph.Blocks) {
		return
	}
	block := a.graph.Blocks[site.ID.Block]
	branch, ok := block.Terminator.(*cfg.Branch)
	if !ok || branch == nil || branch.ConditionID == 0 {
		return
	}
	condition, _ := a.source.Node(branch.ConditionID).(thir.Expr)
	if typednil.IsNil(condition) {
		return
	}
	for _, implied := range a.impliedVariants(condition, edge == cfg.EdgeTrue, *state, events) {
		filtered := copyFlowState(*state)
		filtered.variants = nil
		restrictVariantFact(&filtered, implied.variant)
		for _, call := range events.calls {
			if call.order > implied.order {
				a.invalidateCall(call.call, &filtered)
			}
		}
		if !filtered.isReachable {
			state.isReachable = false
			return
		}
		if len(filtered.variants) > 0 {
			restrictVariantFact(state, filtered.variants[0])
		}
	}
}

func (a *flowAnalyzer) impliedVariants(expr thir.Expr, truth bool, state flowState, events *flowEvents) []edgeVariantFact {
	if typednil.IsNil(expr) {
		return nil
	}
	if test, found := a.result.CaseTest(ast.NodeID(expr.SourceInfo().NodeID)); found {
		subject, _ := a.source.Node(ir.NodeID(test.SubjectID)).(thir.Expr)
		resolution := a.resolve(subject, state)
		if !resolution.IsStable || len(resolution.StorageOrigins) == 0 {
			if test.Family == typeinfo.VariantFamilyOptional {
				a.ctx.Diagnostics.Add(unstableOptionalNarrowingAt(subject.SourceInfo().Location))
			}
			return nil
		}
		cases := []int{test.Case}
		if truth != test.MatchesWhenTrue {
			cases = variantCasesExcept(test.CaseCount, test.Case)
		}
		order := 0
		if events != nil {
			order = events.tests[expr.SourceInfo().NodeID]
		}
		return []edgeVariantFact{{variant: variantStateFact{
			origins: place.VariantPayloadOrigins(resolution.StorageOrigins, test.PayloadPath),
			cases:   cases, caseCount: test.CaseCount,
			dependencies: append([]*symbols.Symbol(nil), resolution.Dependencies...),
		}, order: order}}
	}
	switch node := expr.(type) {
	case *thir.Unary:
		if node.Op == "!" {
			return a.impliedVariants(node.Value, !truth, state, events)
		}
	case *thir.Binary:
		switch node.Op {
		case "&&":
			if truth {
				return a.constrainEdgeVariantFacts(state, events,
					a.impliedVariants(node.Left, true, state, events),
					a.impliedVariants(node.Right, true, state, events))
			}
			return alternateEdgeVariantFacts(a.impliedVariants(node.Left, false, state, events), a.impliedVariants(node.Right, false, state, events))
		case "||":
			if truth {
				return alternateEdgeVariantFacts(a.impliedVariants(node.Left, true, state, events), a.impliedVariants(node.Right, true, state, events))
			}
			return a.constrainEdgeVariantFacts(state, events,
				a.impliedVariants(node.Left, false, state, events),
				a.impliedVariants(node.Right, false, state, events))
		}
	}
	return nil
}

func (a *flowAnalyzer) constrainEdgeVariantFacts(state flowState, events *flowEvents, left, right []edgeVariantFact) []edgeVariantFact {
	merged := make([]edgeVariantFact, len(left))
	for index, fact := range left {
		merged[index] = edgeVariantFact{variant: variantStateFact{
			origins: place.CloneOrigins(fact.variant.origins), cases: append([]int(nil), fact.variant.cases...),
			caseCount: fact.variant.caseCount, dependencies: append([]*symbols.Symbol(nil), fact.variant.dependencies...),
		}, order: fact.order}
	}
	for _, candidate := range right {
		found := false
		for index := range merged {
			if !place.AreSameOrigins(merged[index].variant.origins, candidate.variant.origins) {
				continue
			}
			found = true
			if a.variantFactInvalidatedBetween(state, events, merged[index], candidate.order) {
				candidate.variant.origins = place.CloneOrigins(candidate.variant.origins)
				candidate.variant.cases = append([]int(nil), candidate.variant.cases...)
				candidate.variant.dependencies = append([]*symbols.Symbol(nil), candidate.variant.dependencies...)
				merged[index] = candidate
				break
			}
			merged[index].variant.cases = intersectCaseSets(merged[index].variant.cases, candidate.variant.cases)
			merged[index].variant.dependencies = mergeDependencies(merged[index].variant.dependencies, candidate.variant.dependencies)
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

func (a *flowAnalyzer) variantFactInvalidatedBetween(state flowState, events *flowEvents, fact edgeVariantFact, before int) bool {
	if events == nil || before <= fact.order {
		return false
	}
	filtered := copyFlowState(state)
	filtered.variants = nil
	restrictVariantFact(&filtered, fact.variant)
	if !filtered.isReachable {
		return false
	}
	for _, call := range events.calls {
		if call.order > fact.order && call.order < before {
			a.invalidateCall(call.call, &filtered)
		}
	}
	_, found := variantFact(filtered.variants, fact.variant.origins)
	return !found
}
