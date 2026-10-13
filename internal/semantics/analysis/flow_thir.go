package analysis

import (
	"compiler/internal/diagnostics"
	graphcore "compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/problems"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"compiler/pkg/typednil"
)

type flowCallEvent struct {
	order int
	call  *thir.Call
}

type flowEvents struct {
	next  int
	tests map[source.NodeID]int
	calls []flowCallEvent
}

// flowAnalyzer owns path-sensitive semantics over immutable THIR and CFG.
// Base typing has already resolved names, types, calls, places, and control
// evidence; this phase only refines those facts along control-flow edges.
type flowAnalyzer struct {
	diagnostics   *diagnostics.DiagnosticBag
	moduleScope   *symbols.Scope
	source        *thir.Module
	function      *thir.Function
	functionScope *symbols.Scope
	graph         *cfg.ControlFlowGraph
	result        *Module
	inStates      map[cfg.SiteID]flowState

	state  *flowState
	events *flowEvents
}

func runFlow(diag *diagnostics.DiagnosticBag, input Input, result *Module) {
	source, graphs, moduleScope := input.Source, input.CFG, input.Scope
	if diag == nil || source == nil || graphs == nil || result == nil {
		return
	}
	for _, graph := range graphs.Functions {
		if graph == nil {
			continue
		}
		function := source.FunctionByID(graph.FunctionID)
		if function == nil || function.Symbol == nil || function.Symbol.Scope == nil {
			continue
		}
		analyzer := &flowAnalyzer{
			diagnostics: diag, moduleScope: moduleScope, source: source, function: function,
			functionScope: function.Symbol.Scope, graph: graph, result: result,
			inStates: make(map[cfg.SiteID]flowState),
		}
		analyzer.run()
	}
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
			cases := make([]int, typeinfo.OptionalLayerCount(typ))
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
	events := &flowEvents{tests: make(map[source.NodeID]int)}
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

// analyze runs flow analysis for expr and, when the type checker typed it as
// the payload of a narrowed place, verifies that read.
func (a *flowAnalyzer) analyze(expr thir.Expr) {
	if typednil.IsNil(expr) {
		return
	}
	a.result.forgetPayload(expr.SourceInfo().NodeID)
	expr.AnalyzeFlow(a)
	if carrier := expr.CaseCarrierType(); carrier != nil {
		a.casePayload(expr, carrier)
	}
}

// finish records where expr's value lives. Payload reads of a narrowed place
// are settled by casePayload afterwards.
func (a *flowAnalyzer) finish(expr thir.Expr) {
	if !typednil.IsNil(expr) && expr.ExprType() != nil {
		a.recordResolution(expr, a.resolve(expr, *a.state))
	}
}

func unstableOptionalNarrowingAt(location *source.Location) *diagnostics.Diagnostic {
	return diagnostics.NewError("optional narrowing subject is not a stable place").
		WithPrimaryLabel(location, "this expression can change between the test and use").
		WithCode(diagnostics.ErrUnstableNarrowing).
		Help("bind the expression or index to a direct local before testing it")
}

func (a *flowAnalyzer) AnalyzeBlock(*thir.Block)             {}
func (a *flowAnalyzer) AnalyzeBreak(*thir.Break)             {}
func (a *flowAnalyzer) AnalyzeContinue(*thir.Continue)       {}
func (a *flowAnalyzer) AnalyzeInvalidStmt(*thir.InvalidStmt) {}

func (a *flowAnalyzer) AnalyzeBinding(statement *thir.Binding) {
	typ, _ := symbols.GetSymbolType(statement.Symbol)
	a.analyze(statement.Value)
	if statement.Symbol != nil {
		a.updateOriginPlace([]place.Origin{{Root: statement.Symbol}}, typ, statement.Value, copyFlowState(*a.state), a.state)
	}
}

func (a *flowAnalyzer) AnalyzeDestructure(statement *thir.Destructure) {
	a.analyze(statement.Value)
	strct, _ := typeinfo.Underlying(statement.Value.ExprType()).(*typeinfo.StructType)
	origins := a.projectedBaseOrigins(a.resolve(statement.Value, *a.state), statement.Value)
	for _, binding := range statement.Bindings {
		// The new local holds whatever reference or raw pointer its field held.
		fieldOrigins := place.FieldOrigins(origins, strct.Fields[binding.Field].Name)
		storage := []place.Origin{{Root: binding.Symbol}}
		valueOrigins := fieldOrigins
		var held *[]originStateFact
		if _, _, isReference := typeinfo.ReferenceValueTarget(binding.Type); isReference {
			held = &a.state.references
		} else if _, isRaw := typeinfo.Underlying(binding.Type).(*typeinfo.RawPtrType); isRaw {
			held = &a.state.rawPointers
		}
		if held != nil {
			if targets := originValues(*held, fieldOrigins); len(targets) > 0 {
				valueOrigins = targets
			}
			*held = setOriginFact(*held, storage, valueOrigins)
		}
		a.result.mergeOrigins(binding.Source.NodeID, storage, valueOrigins)
	}
}

func (a *flowAnalyzer) AnalyzeExprStmt(statement *thir.ExprStmt) {
	a.analyze(statement.Value)
}

func (a *flowAnalyzer) AnalyzeAssign(statement *thir.Assign) {
	a.analyze(statement.Target)
	// Preserve state before the write for origin and ownership updates.
	sourceState := copyFlowState(*a.state)
	// Resolve the destination storage place.
	resolution := a.resolve(statement.Target, *a.state)
	// The value is evaluated before the write, so it still sees what the
	// destination held.
	a.analyze(statement.Value)
	// Invalidate refinements invalidated by writing to the destination.
	invalidateVariantOrigins(a.state, resolution.StorageOrigins)
	// Publish the destination's post-write origin state.
	a.updateOriginPlace(resolution.StorageOrigins, statement.Target.ExprType(), statement.Value, sourceState, a.state)
}

func (a *flowAnalyzer) AnalyzeReturn(statement *thir.Return) {
	a.analyze(statement.Value)
}

func (a *flowAnalyzer) AnalyzeIf(statement *thir.If) {
	a.analyze(statement.Condition)
}

func (a *flowAnalyzer) AnalyzeFor(statement *thir.For) {
	if statement.Checked != nil {
		return
	}
	a.analyze(statement.Iterable)
	a.analyze(statement.Condition)
}

func (a *flowAnalyzer) AnalyzeMatch(statement *thir.Match) {
	a.analyze(statement.Subject)
}

func (a *flowAnalyzer) AnalyzeInvalidExpr(expr *thir.InvalidExpr) {
	a.finish(expr)
}
func (a *flowAnalyzer) AnalyzeNumberLiteral(expr *thir.NumberLiteral) {
	a.finish(expr)
}
func (a *flowAnalyzer) AnalyzeStringLiteral(expr *thir.StringLiteral) {
	a.finish(expr)
}
func (a *flowAnalyzer) AnalyzeByteLiteral(expr *thir.ByteLiteral) {
	a.finish(expr)
}
func (a *flowAnalyzer) AnalyzeCharLiteral(expr *thir.CharLiteral) {
	a.finish(expr)
}
func (a *flowAnalyzer) AnalyzeBoolLiteral(expr *thir.BoolLiteral) {
	a.finish(expr)
}
func (a *flowAnalyzer) AnalyzeNoneLiteral(expr *thir.NoneLiteral) {
	a.finish(expr)
}
func (a *flowAnalyzer) AnalyzeIdent(expr *thir.Ident) { a.finish(expr) }

// casePayload verifies a read the type checker typed as the payload of a
// narrowed place: the place must hold exactly that case here. The read is then
// recorded as a payload access, which lowering turns into an in-place load.
func (a *flowAnalyzer) casePayload(expr thir.Expr, carrier typeinfo.Type) {
	payloadType := expr.ExprType()
	resolved := a.resolve(expr, *a.state)
	storage := resolved.StorageOrigins
	// The read goes through one payload per narrowed level until it reaches
	// the type the checker gave it.
	var cases []int
	for read := carrier; !typeinfo.IsSameType(read, payloadType); {
		descriptor, _ := typeinfo.VariantDescriptorOf(read)
		caseIndex, exact := provenVariantCase(a.state.variants, place.VariantPayloadOrigins(storage, cases), len(descriptor.Cases))
		if !exact || descriptor.Cases[caseIndex].Payload == nil {
			location := expr.SourceInfo().Location
			switch {
			case descriptor.Family != typeinfo.VariantFamilyOptional:
				a.diagnostics.AddError(diagnostics.ErrInvalidExpression,
					"enum case is not proven here", location, "the value may have changed since it was tested").
					Help("test it again, or bind what you need before changing it")
			case expr.ExprPlace() != nil && !resolved.IsStable:
				a.diagnostics.Add(unstableOptionalNarrowingAt(location))
			default:
				a.diagnostics.Add(problems.OptionalPayloadProof(location))
			}
			return
		}
		cases = append(cases, caseIndex)
		read = descriptor.Cases[caseIndex].Payload
	}
	payload := place.VariantPayloadOrigins(storage, cases)
	resolution := place.Resolution{StorageOrigins: storage, IsStable: true}
	resolution.ValueOrigins = a.resolveStored(payloadType, place.Resolution{
		StorageOrigins: payload, ValueOrigins: place.CloneOrigins(payload),
	}, *a.state).ValueOrigins
	a.recordPayload(expr, resolution, cases)
	a.recordResolution(expr, resolution)
}

func (a *flowAnalyzer) AnalyzeQualifiedIdent(expr *thir.QualifiedIdent) { a.finish(expr) }

func (a *flowAnalyzer) AnalyzeField(expr *thir.Field) {
	a.analyze(expr.Base)
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeIndex(expr *thir.Index) {
	a.analyze(expr.Base)
	a.analyze(expr.Index)
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeRange(expr *thir.Range) {
	if !typednil.IsNil(expr.Start) {
		a.analyze(expr.Start)
	}
	if !typednil.IsNil(expr.End) {
		a.analyze(expr.End)
	}
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeStructLiteral(expr *thir.StructLiteral) {
	for _, field := range expr.Fields {
		a.analyze(field.Value)
	}
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeVariant(expr *thir.Variant) {
	a.analyze(expr.Payload)
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeArrayLiteral(expr *thir.ArrayLiteral) {
	for _, value := range expr.Values {
		a.analyze(value)
	}
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeAddress(expr *thir.Address) {
	a.analyze(expr.Value)
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeUnary(expr *thir.Unary) {
	a.analyze(expr.Value)
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeBinary(expr *thir.Binary) {
	a.analyze(expr.Left)
	a.analyze(expr.Right)
	a.recordCaseTest(expr)
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeIs(expr *thir.Is) {
	a.analyze(expr.Value)
	a.recordCaseTest(expr)
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeCall(expr *thir.Call) {
	a.analyze(expr.Callee)
	for _, argument := range expr.Args {
		a.analyze(argument)
	}
	a.invalidateCall(expr, a.state)
	if a.events != nil {
		a.events.next++
		a.events.calls = append(a.events.calls, flowCallEvent{order: a.events.next, call: expr})
	}
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeFree(expr *thir.Free) {
	a.analyze(expr.Value)
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzePrint(expr *thir.Print) {
	a.analyze(expr.Value)
	a.finish(expr)
}

func (a *flowAnalyzer) AnalyzeCast(expr *thir.Cast) {
	a.analyze(expr.Value)
	a.finish(expr)
}

// recordCaseTest notes the payload path a case test reads its subject
// through, and orders the test among the site's events.
func (a *flowAnalyzer) recordCaseTest(expr thir.Expr) {
	test := thir.CaseTestOf(expr)
	if test == nil {
		return
	}
	id := expr.SourceInfo().NodeID
	if payload, ok := a.result.Payload(test.SubjectID); ok && payload.AppliesTo(a.result.StorageOrigins(test.SubjectID)) {
		a.result.expressions.testPaths[id] = payload.Cases
	}
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
	a.result.recordPayload(expr.SourceInfo().NodeID, PayloadAccess{
		CarrierOrigins: place.CloneOrigins(resolution.StorageOrigins), Cases: append([]int(nil), cases...), IsDirect: isDirect,
	})
}

func (a *flowAnalyzer) recordResolution(expr thir.Expr, resolution place.Resolution) {
	a.result.recordOrigins(expr.SourceInfo().NodeID, resolution.StorageOrigins, resolution.ValueOrigins)
}

func (a *flowAnalyzer) resolve(expr thir.Expr, state flowState) place.Resolution {
	if typednil.IsNil(expr) {
		return place.Resolution{}
	}
	switch node := expr.(type) {
	case *thir.Address:
		resolved := a.resolve(node.Value, state)
		if payload, ok := a.result.Payload(node.Value.SourceInfo().NodeID); ok {
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
		origins = appendIndirectOrigins(origins, node.Base.ExprType())
		origins = place.FieldOrigins(origins, node.Name)
		return a.resolveStored(node.ExprType(), place.Resolution{
			StorageOrigins: origins, ValueOrigins: place.CloneOrigins(origins),
			Dependencies: append([]*symbols.Symbol(nil), base.Dependencies...), IsStable: base.IsStable && len(origins) > 0,
		}, state)
	case *thir.Index:
		base := a.resolve(node.Base, state)
		origins := appendIndirectOrigins(a.projectedBaseOrigins(base, node.Base), node.Base.ExprType())
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
	if _, _, isReference := typeinfo.ReferenceTarget(typeinfo.Underlying(expr.ExprType())); isReference {
		return origins
	}
	if payload, ok := a.result.Payload(expr.SourceInfo().NodeID); ok {
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
	fn, _ := typeinfo.Underlying(call.Callee.ExprType()).(*typeinfo.FuncType)
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
		slots := make([]AggregateSlot, 0, len(expression.Fields))
		semantic, _ := typeinfo.Underlying(typ).(*typeinfo.StructType)
		for _, field := range expression.Fields {
			if typednil.IsNil(field.Value) || semantic == nil || field.Index < 0 || field.Index >= len(semantic.Fields) {
				continue
			}
			semanticField := semantic.Fields[field.Index]
			slots = append(slots, AggregateSlot{
				ValueExpr:  field.Value,
				Projection: place.OriginProjection{Kind: place.OriginField, Field: semanticField.Name},
			})
			a.updateOriginPlace(place.FieldOrigins(storage, semanticField.Name), semanticField.Type, field.Value, sourceState, state)
		}
		a.result.recordAggregateSlots(expression.Source.NodeID, slots)
	case *thir.Variant:
		if typednil.IsNil(expression.Payload) || expression.Case < 0 {
			return
		}
		a.result.recordAggregateSlots(expression.Source.NodeID, []AggregateSlot{{
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
		origins := source.ValueOrigins
		// A payload read copies what the payload stores, not what its carrier does.
		if payload, isPayload := a.result.Payload(value.SourceInfo().NodeID); isPayload {
			origins = place.VariantPayloadOrigins(source.StorageOrigins, payload.Cases)
		}
		a.copyStoredOriginPlace(storage, origins, typ, sourceState, state)
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
	if strct, isStruct := typeinfo.Underlying(typ).(*typeinfo.StructType); isStruct && strct != nil {
		for _, field := range strct.Fields {
			a.copyStoredOriginPlace(place.FieldOrigins(destination, field.Name), place.FieldOrigins(source, field.Name), field.Type, sourceState, state)
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
	fn, _ := typeinfo.Underlying(call.Callee.ExprType()).(*typeinfo.FuncType)
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
	if a.moduleScope != nil {
		for _, symbol := range a.moduleScope.Symbols() {
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
	// A payload switch decides the case of the payload its path leads to.
	term, _ := a.graph.Blocks[site.ID.Block].Terminator.(*cfg.SwitchVariant)
	switched := match.EnumType
	for _, caseIndex := range term.Path {
		descriptor, _ := typeinfo.VariantDescriptorOf(switched)
		switched = descriptor.Cases[caseIndex].Payload
	}
	descriptor, _ := typeinfo.VariantDescriptorOf(switched)
	resolution := a.resolve(match.Subject, *state)
	if resolution.IsStable && len(resolution.StorageOrigins) > 0 {
		restrictVariantFact(state, variantStateFact{
			origins: place.VariantPayloadOrigins(resolution.StorageOrigins, term.Path),
			cases:   []int{edge.Case}, caseCount: len(descriptor.Cases),
			dependencies: append([]*symbols.Symbol(nil), resolution.Dependencies...),
		})
	}
}

func (a *flowAnalyzer) applyConditionEdge(site *cfg.Site, edge cfg.EdgeKind, state *flowState, events *flowEvents) {
	if site == nil || state == nil || edge != cfg.EdgeTrue && edge != cfg.EdgeFalse || site.ID.Block < 0 || site.ID.Block >= len(a.graph.Blocks) {
		return
	}
	block := a.graph.Blocks[site.ID.Block]
	branch, ok := block.Terminator.(*cfg.Branch)
	if !ok || branch == nil || !branch.ConditionID.IsValid() {
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
	if test := thir.CaseTestOf(expr); test != nil {
		subject, _ := a.source.Node(test.SubjectID).(thir.Expr)
		resolution := a.resolve(subject, state)
		if !resolution.IsStable || len(resolution.StorageOrigins) == 0 {
			if test.Family == typeinfo.VariantFamilyOptional {
				a.diagnostics.Add(unstableOptionalNarrowingAt(subject.SourceInfo().Location))
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
			origins: place.VariantPayloadOrigins(resolution.StorageOrigins, a.result.expressions.testPaths[expr.SourceInfo().NodeID]),
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
