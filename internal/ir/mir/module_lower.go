package mir

import (
	"fmt"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/exprlower"
	"compiler/internal/ir/thir"
	"compiler/internal/ir/typelower"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/constantresult"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

// LoweringInput is the complete phase handoff from typed source semantics to
// MIR. Keeping module state outside this package prevents a module/MIR cycle.
type LoweringInput struct {
	Types       *ir.TypeTable
	Diagnostics *diagnostics.DiagnosticBag
	Source      *thir.Module
	CFG         *cfg.Module
	Flow        *flowresult.Result
	Ownership   ownershipresult.Result
	Scope       *symbols.Scope
	Constants   *constantresult.Result
	ModuleID    moduleid.ID
	Entry       bool
}

type lowerer struct {
	input           LoweringInput
	module          *Module
	fn              *Function
	sourceFn        *thir.Function
	tmp             int
	current         *Block
	location        *source.Location
	temporaryDrops  []ValueRef
	cleanup         *ownershipresult.CleanupPlan
	symbolValues    map[symbols.SymbolID]*RefName
	constantEnv     map[string]constvalue.Value
	variantEntries  map[*cfg.Block]variantEntry
	variantSubjects map[ir.NodeID]ValueRef
}

type variantEntry struct {
	switchID ir.NodeID
	arm      *thir.MatchArm
}

func GenerateMIR(input LoweringInput) *Module {
	if input.Types == nil || input.Source == nil || input.CFG == nil {
		return nil
	}
	out := &Module{
		FilePath:        input.Source.FilePath,
		Name:            input.Source.Name,
		Types:           input.Types,
		StaticData:      make([]*StaticEntry, 0),
		InterfaceThunks: make([]*InterfaceThunk, 0),
		Funcs:           make([]*Function, 0, len(input.Source.Functions)),
	}

	// All semantic constant types must be interned before ABI-key lookup below.
	if input.Scope != nil {
		for _, symbol := range input.Scope.Symbols() {
			if symbol != nil && symbol.Kind == symbols.SymbolConst {
				semanticType, _ := symbols.GetSymbolType(symbol)
				if typelower.Type(input.Types, input.Diagnostics, semanticType) == ir.InvalidType {
					return nil
				}
			}
		}
		for _, symbol := range input.Scope.Symbols() {
			if symbol == nil || symbol.Kind != symbols.SymbolConst {
				continue
			}
			var value constvalue.Value
			if input.Constants != nil {
				value = input.Constants.Published(symbol.ID)
			}
			internConstantStrings(out, value)
			if entry, ok := staticEntryForConst(input.Types, symbol, value); ok {
				out.StaticData = append(out.StaticData, entry)
			}
		}
	}

	for _, sourceFn := range input.Source.Functions {
		if sourceFn == nil || sourceFn.Symbol == nil {
			continue
		}
		if sourceFn.Body == nil {
			out.Funcs = append(out.Funcs, functionSignature(input, sourceFn, nil))
			continue
		}
		graph := input.CFG.Function(sourceFn.Source.NodeID)
		if graph == nil {
			return nil
		}
		fn, ok := lowerCFGFunction(out, input, sourceFn, graph, input.Ownership[sourceFn.Source.NodeID])
		if !ok {
			return nil
		}
		out.Funcs = append(out.Funcs, fn)
	}
	return out
}

func functionSignature(input LoweringInput, sourceFn *thir.Function, blocks []*Block) *Function {
	params := make([]ir.Param, 0, len(sourceFn.Params))
	for _, parameter := range sourceFn.Params {
		name := ""
		var symbolID symbols.SymbolID
		if parameter.Symbol != nil {
			name = exprlower.SymbolName(input.ModuleID, input.Entry, parameter.Symbol)
			symbolID = parameter.Symbol.ID
		}
		params = append(params, ir.Param{
			Name: name, Type: typelower.Type(input.Types, input.Diagnostics, parameter.Type), SymbolID: symbolID,
		})
	}
	name, _ := exprlower.CallableName(input.ModuleID, input.Entry, sourceFn.Symbol)
	return &Function{
		Name:       name,
		Params:     params,
		ReturnType: typelower.ReturnType(input.Types, input.Diagnostics, sourceFn.ReturnType),
		Blocks:     blocks,
		Location:   sourceFn.Source.Location,
	}
}

// lowerCFGFunction converts finalized CFG topology into MIR. THIR supplies node
// meaning while CFG remains sole owner of execution order and branch targets.
func lowerCFGFunction(mod *Module, input LoweringInput, sourceFn *thir.Function, graph *cfg.ControlFlowGraph, cleanup *ownershipresult.CleanupPlan) (*Function, bool) {
	if mod == nil || sourceFn == nil || graph == nil || graph.Entry == nil {
		return nil, false
	}
	fn := functionSignature(input, sourceFn, make([]*Block, 0, len(graph.Blocks)))
	l := &lowerer{
		input: input, module: mod, fn: fn, sourceFn: sourceFn, cleanup: cleanup,
		symbolValues:    make(map[symbols.SymbolID]*RefName),
		constantEnv:     make(map[string]constvalue.Value),
		variantEntries:  make(map[*cfg.Block]variantEntry),
		variantSubjects: make(map[ir.NodeID]ValueRef),
	}
	for _, parameter := range fn.Params {
		if parameter.SymbolID != 0 {
			l.symbolValues[parameter.SymbolID] = &RefName{Name: parameter.Name, Type: parameter.Type}
		}
	}

	blocks := make(map[*cfg.Block]*Block, len(graph.Blocks))
	for _, sourceBlock := range graph.Blocks {
		if sourceBlock == nil || sourceBlock == graph.Exit || !sourceBlock.Reachable {
			continue
		}
		block := &Block{ID: sourceBlock.ID, Instrs: make([]Instr, 0)}
		blocks[sourceBlock] = block
		fn.Blocks = append(fn.Blocks, block)
		if sourceBlock == graph.Entry {
			fn.EntryID = block.ID
		}
	}
	if blocks[graph.Entry] == nil {
		return nil, false
	}

	for _, sourceBlock := range graph.Blocks {
		term, switched := sourceBlock.Terminator.(*cfg.SwitchVariant)
		if !switched {
			continue
		}
		match, ok := input.Source.Node(term.NodeID).(*thir.Match)
		if !ok || len(match.Arms) != len(term.Targets) {
			return nil, false
		}
		for index, target := range term.Targets {
			arm := &match.Arms[index]
			if blocks[target.Target] == nil || arm.Case != target.Case || arm.Body == nil {
				return nil, false
			}
			l.variantEntries[target.Target] = variantEntry{switchID: term.NodeID, arm: arm}
		}
	}

	for _, sourceBlock := range graph.Blocks {
		block := blocks[sourceBlock]
		if block == nil {
			continue
		}
		l.current = block
		l.location = sourceBlock.Location
		if entry, found := l.variantEntries[sourceBlock]; found && !l.lowerVariantBindings(entry) {
			return nil, false
		}
		if loop, ok := input.Source.Node(sourceBlock.NodeID).(*thir.For); ok {
			if !l.lowerIterationSegment(loop, sourceBlock.Origin) {
				return nil, false
			}
		}
		for _, site := range sourceBlock.Sites {
			switch site.Kind {
			case cfg.SiteStatement:
				if !l.lowerCFGStmt(input.Source.Node(site.NodeID)) {
					return nil, false
				}
			case cfg.SiteScopeExit:
				if l.cleanup != nil {
					l.location = site.Location
					l.appendPlannedDrops(l.cleanup.AfterScope[site.ID], &block.Instrs)
				}
			}
		}
		if !l.lowerCFGTerminator(sourceBlock, graph.Exit, blocks) {
			return nil, false
		}
	}
	return fn, true
}

func (l *lowerer) expressionContext() exprlower.Context {
	return exprlower.Context{
		Types: l.input.Types, Diagnostics: l.input.Diagnostics, Source: l.input.Source,
		Flow: l.input.Flow, ModuleID: l.input.ModuleID, Entry: l.input.Entry,
	}
}

func (l *lowerer) sourceExpr(expr thir.Expr, expected typeinfo.Type) ir.Expr {
	return ir.FoldExpr(l.input.Types, exprlower.Lower(l.expressionContext(), expr, expected), l.constantEnv)
}

func (l *lowerer) sourcePlace(expr thir.Expr) *ir.Place {
	return ir.FoldPlace(l.input.Types, exprlower.LowerPlace(l.expressionContext(), expr), l.constantEnv)
}

func (l *lowerer) symbolRef(symbol *symbols.Symbol) *ir.Ident {
	if symbol == nil {
		return nil
	}
	return &ir.Ident{
		Name:     exprlower.SymbolName(l.input.ModuleID, l.input.Entry, symbol),
		Type:     typelower.Type(l.input.Types, l.input.Diagnostics, symbol.Type),
		SymbolID: symbol.ID, SourceInfo: ir.SourceInfo{Location: l.location},
	}
}

func (l *lowerer) lowerIterationSegment(loop *thir.For, origin cfg.BlockOrigin) bool {
	if loop == nil || loop.Iteration == nil {
		return true
	}
	switch plan := loop.Iteration.(type) {
	case *thir.RangeIteration:
		rangeExpr, ok := loop.Iterable.(*thir.Range)
		if !ok || rangeExpr.Start == nil || rangeExpr.End == nil {
			return false
		}
		switch origin {
		case cfg.BlockLoopInit:
			l.assignSymbol(plan.Cursor, l.sourceExpr(rangeExpr.Start, plan.ElementType))
			l.assignSymbol(plan.Limit, l.sourceExpr(rangeExpr.End, plan.ElementType))
			if plan.Ordinal != nil {
				l.assignSymbol(plan.Ordinal, l.integerLiteral("0", plan.Ordinal.Type))
			}
		case cfg.BlockLoopBody:
			if loop.Index != nil {
				l.assignSymbol(loop.Index, l.symbolRef(plan.Ordinal))
			}
			l.assignSymbol(loop.Value, l.symbolRef(plan.Cursor))
		case cfg.BlockLoopLatch:
			l.incrementSymbol(plan.Cursor)
			if plan.Ordinal != nil {
				l.incrementSymbol(plan.Ordinal)
			}
		}
	case *thir.SequenceIteration:
		switch origin {
		case cfg.BlockLoopInit:
			l.assignSymbol(plan.Carrier, exprlower.LowerImplicitReference(l.expressionContext(), loop.Iterable, plan.CarrierType))
			l.assignSymbol(plan.Cursor, l.integerLiteral("0", plan.Cursor.Type))
		case cfg.BlockLoopBody:
			if loop.Index != nil {
				l.assignSymbol(loop.Index, l.symbolRef(plan.Cursor))
			}
			elementType := typelower.Type(l.input.Types, l.input.Diagnostics, plan.ElementType)
			value := &ir.Load{Place: &ir.Place{
				Root: l.symbolRef(plan.Carrier),
				Projections: []ir.PlaceProjection{{
					Kind: ir.PlaceProjectionIndex, Index: l.symbolRef(plan.Cursor), Type: elementType, Location: l.location,
				}},
				Type: elementType, Location: l.location,
			}}
			l.assignSymbol(loop.Value, value)
		case cfg.BlockLoopLatch:
			l.incrementSymbol(plan.Cursor)
		}
	default:
		return false
	}
	return true
}

func (l *lowerer) iterationCondition(loop *thir.For) ir.Expr {
	if loop.Condition != nil {
		return l.sourceExpr(loop.Condition, &typeinfo.BoolType{})
	}
	boolType := typelower.Type(l.input.Types, l.input.Diagnostics, &typeinfo.BoolType{})
	switch plan := loop.Iteration.(type) {
	case *thir.RangeIteration:
		return &ir.Binary{Op: "<", Left: l.symbolRef(plan.Cursor), Right: l.symbolRef(plan.Limit), Type: boolType, SourceInfo: ir.SourceInfo{Location: l.location}}
	case *thir.SequenceIteration:
		cursorType := typelower.Type(l.input.Types, l.input.Diagnostics, plan.Cursor.Type)
		return &ir.Binary{
			Op: "<", Left: l.symbolRef(plan.Cursor),
			Right: &ir.Len{Value: l.symbolRef(plan.Carrier), Type: cursorType, SourceInfo: ir.SourceInfo{Location: l.location}},
			Type:  boolType, SourceInfo: ir.SourceInfo{Location: l.location},
		}
	default:
		return nil
	}
}

func (l *lowerer) assignSymbol(symbol *symbols.Symbol, value ir.Expr) {
	if symbol == nil || value == nil {
		return
	}
	name := exprlower.SymbolName(l.input.ModuleID, l.input.Entry, symbol)
	typ := typelower.Type(l.input.Types, l.input.Diagnostics, symbol.Type)
	l.symbolValues[symbol.ID] = &RefName{Name: name, Type: typ, Location: l.location}
	ref := l.lowerExpr(value, &l.current.Instrs)
	if existing, ok := ref.(*RefName); ok && existing.Name == name {
		return
	}
	l.appendInstr(&l.current.Instrs, &Assign{Name: name, Value: asValueExpr(ref)})
}

func (l *lowerer) incrementSymbol(symbol *symbols.Symbol) {
	if symbol == nil {
		return
	}
	typ := typelower.Type(l.input.Types, l.input.Diagnostics, symbol.Type)
	l.assignSymbol(symbol, &ir.Binary{
		Op: "+", Left: l.symbolRef(symbol), Right: l.integerLiteral("1", symbol.Type), Type: typ,
		SourceInfo: ir.SourceInfo{Location: l.location},
	})
}

func (l *lowerer) integerLiteral(value string, typ typeinfo.Type) ir.Expr {
	return &ir.IntLit{Value: value, Type: typelower.Type(l.input.Types, l.input.Diagnostics, typ), SourceInfo: ir.SourceInfo{Location: l.location}}
}

func (l *lowerer) lowerVariantBindings(entry variantEntry) bool {
	if l == nil || l.current == nil || entry.arm == nil || entry.arm.Body == nil {
		return false
	}
	subject := l.variantSubjects[entry.switchID]
	if subject == nil {
		return false
	}
	arm := entry.arm
	payloadType := typelower.Type(l.input.Types, l.input.Diagnostics, arm.Payload)
	var drops []int
	payloadDrop := false
	if l.cleanup != nil {
		bodyID := arm.Body.Source.NodeID
		drops = l.cleanup.MatchFieldDrops[bodyID]
		_, payloadDrop = l.cleanup.MatchWholePayloadDrops[bodyID]
	}
	if len(arm.Bindings) == 0 && len(drops) == 0 && !payloadDrop {
		return true
	}
	if payloadType == ir.InvalidType {
		return false
	}
	for _, binding := range arm.Bindings {
		if binding.Symbol == nil || binding.Discard {
			continue
		}
		bindingType := typelower.Type(l.input.Types, l.input.Diagnostics, binding.Type)
		place := variantPayloadPlace(subject, arm.Case, payloadType, arm.Body.Source.Location)
		if binding.Projection == thir.MatchPayloadField {
			place = variantFieldPlace(subject, arm.Case, payloadType, binding.Field, bindingType, arm.Body.Source.Location)
		}
		name := exprlower.SymbolName(l.input.ModuleID, l.input.Entry, binding.Symbol)
		l.symbolValues[binding.Symbol.ID] = &RefName{Name: name, Type: bindingType, Location: arm.Body.Source.Location}
		value := l.load(&l.current.Instrs, place, bindingType, arm.Body.Source.Location)
		l.appendInstr(&l.current.Instrs, &Assign{Name: name, Value: asValueExpr(value)})
	}
	if payloadDrop {
		place := variantPayloadPlace(subject, arm.Case, payloadType, arm.Body.Source.Location)
		value := l.load(&l.current.Instrs, place, payloadType, arm.Body.Source.Location)
		l.appendInstr(&l.current.Instrs, &Drop{Value: value})
	}
	if len(drops) == 0 {
		return true
	}
	payload, ok := l.module.Types.Type(payloadType)
	if !ok || payload.Kind != ir.TypeStruct {
		return false
	}
	for _, fieldIndex := range drops {
		if fieldIndex < 0 || fieldIndex >= len(payload.Fields) {
			return false
		}
		fieldType := payload.Fields[fieldIndex].Type
		place := variantFieldPlace(subject, arm.Case, payloadType, fieldIndex, fieldType, arm.Body.Source.Location)
		value := l.load(&l.current.Instrs, place, fieldType, arm.Body.Source.Location)
		l.appendInstr(&l.current.Instrs, &Drop{Value: value})
	}
	return true
}

func variantFieldPlace(subject ValueRef, caseIndex int, payloadType ir.TypeID, fieldIndex int, fieldType ir.TypeID, location *source.Location) *Place {
	place := variantPayloadPlace(subject, caseIndex, payloadType, location)
	place.Projections = append(place.Projections, PlaceProjection{Kind: PlaceProjectionField, FieldIndex: fieldIndex, Type: fieldType, Location: location})
	place.Type = fieldType
	return place
}

func variantPayloadPlace(subject ValueRef, caseIndex int, payloadType ir.TypeID, location *source.Location) *Place {
	return &Place{
		Root:        subject,
		Projections: []PlaceProjection{{Kind: PlaceProjectionVariantPayload, Case: caseIndex, Type: payloadType, Location: location}},
		Type:        payloadType, Location: location,
	}
}

func staticEntryForConst(types *ir.TypeTable, symbol *symbols.Symbol, value constvalue.Value) (*StaticEntry, bool) {
	if types == nil || symbol == nil || value == nil {
		return nil, false
	}
	typeText := value.TypeText()
	abiKey := typeText
	if variant, ok := value.(*constvalue.VariantConst); ok && variant != nil && variant.NominalIdentity() != "" {
		abiKey = "variant:" + variant.NominalIdentity()
	}
	typ, ok := types.LookupABIKey(abiKey)
	if !ok {
		return nil, false
	}
	return &StaticEntry{Name: fmt.Sprintf("@%s$%d", symbol.Name, symbol.ID), Type: typ, Constant: value}, true
}

func internConstantStrings(module *Module, value constvalue.Value) {
	if module == nil || value == nil {
		return
	}
	switch constant := value.(type) {
	case *constvalue.StringConst:
		if constant != nil {
			module.InternString(constant.Text(), 1)
		}
	case *constvalue.VariantConst:
		if constant != nil {
			for _, field := range constant.FieldValues() {
				internConstantStrings(module, field)
			}
		}
	}
}

func (l *lowerer) lowerCFGStmt(node thir.Node) bool {
	if l == nil || node == nil {
		return true
	}
	previous := l.location
	l.location = node.SourceInfo().Location
	defer func() { l.location = previous }()

	switch statement := node.(type) {
	case *thir.Binding:
		if statement.Value == nil {
			return true
		}
		if discardBinding(statement) {
			return l.lowerExprStatement(statement.Value)
		}
		temporaryMark := len(l.temporaryDrops)
		value := l.sourceExpr(statement.Value, statement.Symbol.Type)
		l.assignSymbol(statement.Symbol, value)
		if statement.Constant {
			name := exprlower.SymbolName(l.input.ModuleID, l.input.Entry, statement.Symbol)
			if folded, ok := ir.ConstValueOf(l.input.Types, value); ok {
				l.constantEnv[name] = folded
			}
		}
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		return true
	case *thir.Return:
		// Return value lowers with terminator so cleanup runs after evaluation.
		return true
	case *thir.ExprStmt:
		return l.lowerExprStatement(statement.Value)
	case *thir.Assign:
		if statement.Target == nil || statement.Value == nil {
			return false
		}
		temporaryMark := len(l.temporaryDrops)
		value := l.lowerExpr(l.sourceExpr(statement.Value, statement.Target.ExprType()), &l.current.Instrs)
		target := l.sourcePlace(statement.Target)
		if target == nil || target.Root == nil {
			return false
		}
		dropTarget := false
		if l.cleanup != nil {
			_, dropTarget = l.cleanup.BeforeAssign[statement.Source.NodeID]
		}
		if ident, direct := target.Root.(*ir.Ident); direct && len(target.Projections) == 0 {
			if dropTarget {
				l.appendInstr(&l.current.Instrs, &Drop{Value: &RefName{Name: ident.Name, Type: target.TypeID(), Location: ident.Origin().Location}})
			}
			l.appendInstr(&l.current.Instrs, &Assign{Name: ident.Name, Value: asValueExpr(value)})
			l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
			return true
		}
		place := l.lowerPlace(target, &l.current.Instrs)
		if dropTarget {
			l.appendInstr(&l.current.Instrs, &Drop{Value: l.load(&l.current.Instrs, place, target.TypeID(), target.Location)})
		}
		l.appendInstr(&l.current.Instrs, &Store{Place: place, Value: value})
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		return true
	case *thir.Break, *thir.Continue, *thir.Block:
		return true
	case *thir.InvalidStmt:
		return false
	default:
		panic(fmt.Sprintf("MIR lowering: unhandled THIR statement %T", node))
	}
}

func discardBinding(binding *thir.Binding) bool {
	if binding == nil || binding.Symbol == nil || binding.Symbol.IsUsed() || binding.Value == nil {
		return false
	}
	if typ, ok := symbols.GetSymbolType(binding.Symbol); ok && typeinfo.OwnershipCapabilityOf(typ).Drop {
		return false
	}
	_, call := binding.Value.(*thir.Call)
	return binding.Symbol.Kind == symbols.SymbolVar && call
}

func (l *lowerer) lowerExprStatement(expr thir.Expr) bool {
	if expr == nil {
		return false
	}
	temporaryMark := len(l.temporaryDrops)
	lowered := l.sourceExpr(expr, nil)
	if l.cleanup != nil {
		if _, drop := l.cleanup.DiscardedValue[expr.SourceInfo().NodeID]; drop {
			value := l.lowerExpr(lowered, &l.current.Instrs)
			l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
			if value != nil {
				l.appendInstr(&l.current.Instrs, &Drop{Value: value})
			}
			return true
		}
	}
	if l.lowerDiscardedExpr(lowered, &l.current.Instrs) {
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		return true
	}
	l.lowerExpr(lowered, &l.current.Instrs)
	l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
	return true
}

func (l *lowerer) appendPlannedDrops(ids []symbols.SymbolID, out *[]Instr) {
	for _, id := range ids {
		ref := l.symbolValues[id]
		if ref != nil {
			l.appendInstr(out, &Drop{Value: &RefName{Name: ref.Name, Type: ref.Type, Location: ref.Location}})
		}
	}
}

func (l *lowerer) flushTemporaryDrops(out *[]Instr, mark int) {
	if out == nil || mark < 0 || mark > len(l.temporaryDrops) {
		return
	}
	for index := len(l.temporaryDrops) - 1; index >= mark; index-- {
		l.appendInstr(out, &Drop{Value: l.temporaryDrops[index]})
	}
	l.temporaryDrops = l.temporaryDrops[:mark]
}

func (l *lowerer) lowerCFGTerminator(sourceBlock, exit *cfg.Block, blocks map[*cfg.Block]*Block) bool {
	if l == nil || sourceBlock == nil || l.current == nil {
		return false
	}
	switch term := sourceBlock.Terminator.(type) {
	case *cfg.Jump:
		if term.Target == exit {
			if l.fn.ReturnType != ir.InvalidType && !l.isVoid(l.fn.ReturnType) {
				return false
			}
			l.setBlockTerm(l.current, &Ret{})
			return true
		}
		target := blocks[term.Target]
		if target == nil {
			return false
		}
		l.setBlockTerm(l.current, &Jump{TargetID: target.ID})
		return true
	case *cfg.Branch:
		thenBlock, elseBlock := blocks[term.TrueTarget], blocks[term.FalseTarget]
		if thenBlock == nil || elseBlock == nil {
			return false
		}
		var condition ir.Expr
		switch statement := l.input.Source.Node(term.NodeID).(type) {
		case *thir.If:
			condition = l.sourceExpr(statement.Condition, &typeinfo.BoolType{})
			l.location = statement.Source.Location
		case *thir.For:
			condition = l.iterationCondition(statement)
			l.location = statement.Source.Location
		default:
			return false
		}
		if condition == nil {
			return false
		}
		temporaryMark := len(l.temporaryDrops)
		lowered := l.lowerExpr(condition, &l.current.Instrs)
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		l.setBlockTerm(l.current, &Branch{Cond: lowered, ThenID: thenBlock.ID, ElseID: elseBlock.ID})
		return true
	case *cfg.SwitchVariant:
		match, ok := l.input.Source.Node(term.NodeID).(*thir.Match)
		if !ok || len(match.Arms) != len(term.Targets) || len(term.Targets) == 0 {
			return false
		}
		l.location = match.Source.Location
		targets := make([]VariantTarget, len(term.Targets))
		for index, target := range term.Targets {
			block := blocks[target.Target]
			if block == nil || match.Arms[index].Case != target.Case {
				return false
			}
			targets[index] = VariantTarget{Case: target.Case, TargetID: block.ID}
		}
		temporaryMark := len(l.temporaryDrops)
		value := l.lowerExpr(l.sourceExpr(match.Subject, match.EnumType), &l.current.Instrs)
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		l.variantSubjects[term.NodeID] = value
		l.setBlockTerm(l.current, &SwitchVariant{Value: value, Targets: targets})
		return true
	case *cfg.Return:
		statement, ok := l.input.Source.Node(term.NodeID).(*thir.Return)
		if !ok {
			return false
		}
		l.location = statement.Source.Location
		temporaryMark := len(l.temporaryDrops)
		var value ValueRef
		if statement.Value != nil {
			value = l.lowerExpr(l.sourceExpr(statement.Value, l.sourceFn.ReturnType), &l.current.Instrs)
		}
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		if l.cleanup != nil {
			l.appendPlannedDrops(l.cleanup.BeforeReturn[term.NodeID], &l.current.Instrs)
		}
		l.setBlockTerm(l.current, &Ret{Value: value})
		return true
	case nil:
		if l.fn.ReturnType != ir.InvalidType && !l.isVoid(l.fn.ReturnType) {
			return false
		}
		l.setBlockTerm(l.current, &Ret{})
		return true
	default:
		panic(fmt.Sprintf("MIR lowering: unhandled CFG terminator %T", sourceBlock.Terminator))
	}
}

func (l *lowerer) isVoid(id ir.TypeID) bool {
	if l == nil || l.module == nil || l.module.Types == nil {
		return false
	}
	typ, ok := l.module.Types.Type(id)
	return ok && typ.Kind == ir.TypeVoid
}

func (l *lowerer) structCastFields(arg ValueRef, target ir.TypeID, location *source.Location, out *[]Instr) ([]ValueRef, bool) {
	if l == nil || l.module == nil || l.module.Types == nil || arg == nil || out == nil {
		return nil, false
	}
	sourceType, ok := l.module.Types.Type(arg.TypeID())
	if !ok || sourceType.Kind != ir.TypeStruct {
		return nil, false
	}
	targetType, ok := l.module.Types.Type(target)
	if !ok || targetType.Kind != ir.TypeStruct || len(sourceType.Fields) != len(targetType.Fields) {
		return nil, false
	}
	sourceFields := make(map[string]int, len(sourceType.Fields))
	for index, field := range sourceType.Fields {
		if field.Name == "" {
			return nil, false
		}
		if _, exists := sourceFields[field.Name]; exists {
			return nil, false
		}
		sourceFields[field.Name] = index
	}
	fields := make([]ValueRef, 0, len(targetType.Fields))
	for _, field := range targetType.Fields {
		sourceIndex, ok := sourceFields[field.Name]
		if !ok || sourceType.Fields[sourceIndex].Type != field.Type {
			return nil, false
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Field{Base: arg, Index: sourceIndex, Type: field.Type, Location: location}})
		fields = append(fields, &RefName{Name: name, Type: field.Type, Location: location})
		delete(sourceFields, field.Name)
	}
	return fields, len(sourceFields) == 0
}
