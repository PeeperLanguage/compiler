package mir

import (
	"fmt"

	"compiler/internal/constvalue"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/ownershipresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

type lowerer struct {
	module          *Module
	fn              *Function
	tmp             int
	nextBlockID     int
	current         *Block
	location        *source.Location
	temporaryDrops  []ValueRef
	cleanup         *ownershipresult.CleanupPlan
	symbolValues    map[symbols.SymbolID]*RefName
	variantEntries  map[*cfg.Block]variantEntry
	variantSubjects map[ir.NodeID]ValueRef
}

type variantEntry struct {
	switchID  ir.NodeID
	caseBlock hir.VariantCaseBlock
}

func (l *lowerer) isVoid(id ir.TypeID) bool {
	if l == nil || l.module == nil || l.module.Types == nil {
		return false
	}
	typ, ok := l.module.Types.Type(id)
	return ok && typ.Kind == ir.TypeVoid
}

func GenerateMIR(in *hir.Module, graphs *cfg.Module, ownership ownershipresult.Result, scope *symbols.Scope, constValues map[symbols.SymbolID]constvalue.Value) *Module {
	if in == nil || graphs == nil {
		return nil
	}
	out := &Module{
		FilePath:        in.FilePath,
		Name:            in.Name,
		Types:           in.Types,
		StaticData:      make([]*StaticEntry, 0),
		InterfaceThunks: make([]*InterfaceThunk, 0),
		Funcs:           make([]*Function, 0, len(in.Externs)+len(in.Funcs)),
	}

	if scope != nil {
		for _, sym := range scope.Symbols() {
			if sym == nil {
				continue
			}
			if sym.Kind == symbols.SymbolConst {
				value := constValues[sym.ID]
				internConstantStrings(out, value)
				entry, ok := staticEntryForConst(in.Types, sym, value)
				if ok {
					out.StaticData = append(out.StaticData, entry)
				}
			}
		}
	}

	for _, ex := range in.Externs {
		out.Funcs = append(out.Funcs, &Function{
			Name:       ex.Name,
			Params:     append([]ir.Param(nil), ex.Params...),
			ReturnType: ex.ReturnType,
			Blocks:     nil,
			Location:   ex.Location,
		})
	}
	for _, hirFn := range in.Funcs {
		graph := graphs.Function(hirFn.NodeID)
		if graph == nil {
			return nil
		}
		statements := make(map[ir.NodeID]hir.Stmt)
		hir.InspectStmt(hirFn.Body, func(stmt hir.Stmt) bool {
			statements[hir.NodeIDOf(stmt)] = stmt
			return true
		})
		fn, ok := lowerCFGFunction(out, hirFn, graph, statements, ownership[hirFn.NodeID])
		if !ok {
			return nil
		}
		out.Funcs = append(out.Funcs, fn)
	}
	return out
}

// lowerCFGFunction converts one normalized CFG into MIR without rebuilding
// branches or loops from structured HIR.
func lowerCFGFunction(mod *Module, sourceFn *hir.Function, graph *cfg.Graph, statements map[ir.NodeID]hir.Stmt, cleanup *ownershipresult.CleanupPlan) (*Function, bool) {
	if mod == nil || sourceFn == nil || graph == nil || graph.Entry == nil {
		return nil, false
	}
	fn := &Function{
		Name:       sourceFn.Name,
		Params:     append([]ir.Param(nil), sourceFn.Params...),
		ReturnType: sourceFn.ReturnType,
		Blocks:     make([]*Block, 0, len(graph.Blocks)),
		Location:   sourceFn.Location,
	}
	l := &lowerer{
		module: mod, fn: fn, cleanup: cleanup,
		symbolValues:    make(map[symbols.SymbolID]*RefName),
		variantEntries:  make(map[*cfg.Block]variantEntry),
		variantSubjects: make(map[ir.NodeID]ValueRef),
	}
	for _, param := range fn.Params {
		if param.SymbolID != 0 {
			l.symbolValues[param.SymbolID] = &RefName{Name: param.Name, Type: param.Type}
		}
	}
	blocks := make(map[*cfg.Block]*Block, len(graph.Blocks))
	for _, source := range graph.Blocks {
		if source == nil || source == graph.Exit || !source.Reachable {
			continue
		}
		for _, site := range source.Sites {
			if binding, ok := statements[site.NodeID].(*hir.Binding); ok && binding.SymbolID != 0 {
				l.symbolValues[binding.SymbolID] = &RefName{Name: binding.Name, Type: binding.Type, Location: binding.Location}
			}
		}
		block := &Block{ID: source.ID, Instrs: make([]Instr, 0)}
		blocks[source] = block
		fn.Blocks = append(fn.Blocks, block)
		if source == graph.Entry {
			fn.EntryID = block.ID
		}
	}
	if blocks[graph.Entry] == nil {
		return nil, false
	}
	for _, source := range graph.Blocks {
		if source == nil {
			continue
		}
		term, switched := source.Terminator.(*cfg.SwitchVariant)
		if !switched {
			continue
		}
		switchStmt, ok := statements[term.NodeID].(*hir.SwitchVariant)
		if !ok || len(switchStmt.Cases) != len(term.Targets) {
			return nil, false
		}
		for index, target := range term.Targets {
			if blocks[target.Target] == nil || switchStmt.Cases[index].Case != target.Case || switchStmt.Cases[index].Body == nil {
				return nil, false
			}
			l.variantEntries[target.Target] = variantEntry{switchID: term.NodeID, caseBlock: switchStmt.Cases[index]}
			for _, binding := range switchStmt.Cases[index].Bindings {
				if binding.SymbolID != 0 {
					l.symbolValues[binding.SymbolID] = &RefName{Name: binding.Name, Type: binding.Type, Location: switchStmt.Cases[index].Body.Location}
				}
			}
		}
	}
	for _, source := range graph.Blocks {
		block := blocks[source]
		if block == nil {
			continue
		}
		l.current = block
		l.location = source.Location
		if entry, found := l.variantEntries[source]; found && !l.lowerVariantBindings(entry) {
			return nil, false
		}
		for _, site := range source.Sites {
			switch site.Kind {
			case cfg.SiteStatement:
				if !l.lowerCFGStmt(statements[site.NodeID]) {
					return nil, false
				}
			case cfg.SiteScopeExit:
				if l.cleanup != nil {
					l.location = site.Location
					l.appendPlannedDrops(l.cleanup.AfterScope[site.NodeID], &block.Instrs)
				}
			}
		}
		if !l.lowerCFGTerminator(source, graph.Exit, blocks, statements) {
			return nil, false
		}
	}
	return fn, true
}

func (l *lowerer) lowerVariantBindings(entry variantEntry) bool {
	if l == nil || l.current == nil {
		return false
	}
	subject := l.variantSubjects[entry.switchID]
	if subject == nil {
		return false
	}
	var drops []int
	payloadDrop := false
	if l.cleanup != nil && entry.caseBlock.Body != nil {
		drops = l.cleanup.MatchFieldDrops[entry.caseBlock.Body.NodeID]
		_, payloadDrop = l.cleanup.MatchWholePayloadDrops[entry.caseBlock.Body.NodeID]
	}
	if len(entry.caseBlock.Bindings) == 0 && len(drops) == 0 && !payloadDrop {
		return true
	}
	if entry.caseBlock.PayloadType == ir.InvalidType {
		return false
	}
	for _, binding := range entry.caseBlock.Bindings {
		place := variantPayloadPlace(subject, entry.caseBlock)
		if !binding.WholePayload {
			place = variantFieldPlace(subject, entry.caseBlock, binding.FieldIndex, binding.Type)
		}
		value := l.load(&l.current.Instrs, place, binding.Type, entry.caseBlock.Body.Location)
		l.appendInstr(&l.current.Instrs, &Assign{Name: binding.Name, Value: asValueExpr(value)})
	}
	if payloadDrop {
		place := variantPayloadPlace(subject, entry.caseBlock)
		value := l.load(&l.current.Instrs, place, entry.caseBlock.PayloadType, entry.caseBlock.Body.Location)
		l.appendInstr(&l.current.Instrs, &Drop{Value: value})
	}
	if len(drops) == 0 {
		return true
	}
	payload, ok := l.module.Types.Type(entry.caseBlock.PayloadType)
	if !ok || payload.Kind != ir.TypeStruct {
		return false
	}
	for _, fieldIndex := range drops {
		if fieldIndex < 0 || fieldIndex >= len(payload.Fields) {
			return false
		}
		fieldType := payload.Fields[fieldIndex].Type
		place := variantFieldPlace(subject, entry.caseBlock, fieldIndex, fieldType)
		value := l.load(&l.current.Instrs, place, fieldType, entry.caseBlock.Body.Location)
		l.appendInstr(&l.current.Instrs, &Drop{Value: value})
	}
	return true
}

func variantFieldPlace(subject ValueRef, block hir.VariantCaseBlock, fieldIndex int, fieldType ir.TypeID) *Place {
	place := variantPayloadPlace(subject, block)
	place.Projections = append(place.Projections, PlaceProjection{Kind: PlaceProjectionField, FieldIndex: fieldIndex, Type: fieldType, Location: block.Body.Location})
	place.Type = fieldType
	return place
}

func variantPayloadPlace(subject ValueRef, block hir.VariantCaseBlock) *Place {
	return &Place{
		Root: subject,
		Projections: []PlaceProjection{
			{Kind: PlaceProjectionVariantPayload, Case: block.Case, Type: block.PayloadType, Location: block.Body.Location},
		},
		Type: block.PayloadType, Location: block.Body.Location,
	}
}

func staticEntryForConst(types *ir.TypeTable, sym *symbols.Symbol, value constvalue.Value) (*StaticEntry, bool) {
	if types == nil || sym == nil || value == nil {
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
	return &StaticEntry{
		Name:     fmt.Sprintf("@%s$%d", sym.Name, sym.ID),
		Type:     typ,
		Constant: value,
	}, true
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
		if constant == nil {
			return
		}
		for _, field := range constant.FieldValues() {
			internConstantStrings(module, field)
		}
	}
}

func (l *lowerer) lowerCFGStmt(stmt hir.Stmt) bool {
	if l == nil || stmt == nil {
		return true
	}
	prevLoc := l.location
	l.location = hir.LocOf(stmt)
	defer func() {
		l.location = prevLoc
	}()
	switch node := stmt.(type) {
	case *hir.Binding:
		if node.Value == nil {
			return true
		}
		temporaryMark := len(l.temporaryDrops)
		ref := l.lowerExpr(node.Value, &l.current.Instrs)
		if refName, ok := ref.(*RefName); ok && refName.Name == node.Name {
			l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
			return true
		}
		l.appendInstr(&l.current.Instrs, &Assign{Name: node.Name, Value: asValueExpr(ref)})
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		return true
	case *hir.Return:
		// Return value and cleanup lower with the matching CFG terminator so
		// their evaluation occurs once on the edge to function exit.
		return true
	case *hir.ExprStmt:
		temporaryMark := len(l.temporaryDrops)
		if l.cleanup != nil {
			if _, drop := l.cleanup.DiscardedValue[node.ValueNodeID]; drop {
				value := l.lowerExpr(node.Value, &l.current.Instrs)
				l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
				if value != nil {
					l.appendInstr(&l.current.Instrs, &Drop{Value: value})
				}
				return true
			}
		}
		if l.lowerDiscardedExpr(node.Value, &l.current.Instrs) {
			l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
			return true
		}
		l.lowerExpr(node.Value, &l.current.Instrs)
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		return true
	case *hir.Assign:
		temporaryMark := len(l.temporaryDrops)
		value := l.lowerExpr(node.Value, &l.current.Instrs)
		target := node.Target
		if target == nil || target.Root == nil {
			return false
		}
		dropTarget := node.DropTarget
		if l.cleanup != nil {
			if _, planned := l.cleanup.BeforeAssign[node.NodeID]; planned {
				dropTarget = true
			}
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
	case *hir.Invalid:
		return false
	default:
		panic(fmt.Sprintf("MIR lowering: unhandled HIR statement %T", stmt))
	}
}

func (l *lowerer) appendPlannedDrops(ids []symbols.SymbolID, out *[]Instr) {
	if l == nil || out == nil {
		return
	}
	for _, id := range ids {
		ref := l.symbolValues[id]
		if ref == nil {
			continue
		}
		l.appendInstr(out, &Drop{Value: &RefName{Name: ref.Name, Type: ref.Type, Location: ref.Location}})
	}
}

func (l *lowerer) flushTemporaryDrops(out *[]Instr, mark int) {
	if l == nil || out == nil || mark < 0 || mark > len(l.temporaryDrops) {
		return
	}
	for i := len(l.temporaryDrops) - 1; i >= mark; i-- {
		l.appendInstr(out, &Drop{Value: l.temporaryDrops[i]})
	}
	l.temporaryDrops = l.temporaryDrops[:mark]
}

func (l *lowerer) lowerCFGTerminator(source, exit *cfg.Block, blocks map[*cfg.Block]*Block, statements map[ir.NodeID]hir.Stmt) bool {
	if l == nil || source == nil || l.current == nil {
		return false
	}
	switch term := source.Terminator.(type) {
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
		var cond ir.Expr
		switch stmt := statements[term.NodeID].(type) {
		case *hir.If:
			cond = stmt.Cond
			l.location = stmt.Location
		case *hir.For:
			cond = stmt.Cond
			l.location = stmt.Location
		default:
			return false
		}
		temporaryMark := len(l.temporaryDrops)
		lowered := l.lowerExpr(cond, &l.current.Instrs)
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		l.setBlockTerm(l.current, &Branch{Cond: lowered, ThenID: thenBlock.ID, ElseID: elseBlock.ID})
		return true
	case *cfg.SwitchVariant:
		switchStmt, ok := statements[term.NodeID].(*hir.SwitchVariant)
		if !ok || switchStmt == nil || len(switchStmt.Cases) != len(term.Targets) || len(term.Targets) == 0 {
			return false
		}
		l.location = switchStmt.Location
		targets := make([]VariantTarget, len(term.Targets))
		for i, target := range term.Targets {
			block := blocks[target.Target]
			if block == nil || switchStmt.Cases[i].Case != target.Case {
				return false
			}
			targets[i] = VariantTarget{Case: target.Case, TargetID: block.ID}
		}
		temporaryMark := len(l.temporaryDrops)
		value := l.lowerExpr(switchStmt.Value, &l.current.Instrs)
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		l.variantSubjects[term.NodeID] = value
		l.setBlockTerm(l.current, &SwitchVariant{Value: value, Targets: targets})
		return true
	case *cfg.Return:
		ret, ok := statements[term.NodeID].(*hir.Return)
		if !ok || ret == nil {
			return false
		}
		l.location = ret.Location
		temporaryMark := len(l.temporaryDrops)
		value := l.lowerExpr(ret.Value, &l.current.Instrs)
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		for _, cleanup := range ret.Cleanup {
			l.lowerExpr(cleanup, &l.current.Instrs)
		}
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
		panic(fmt.Sprintf("MIR lowering: unhandled CFG terminator %T", source.Terminator))
	}
}

func (l *lowerer) appendInstr(out *[]Instr, instr Instr) {
	if out == nil || instr == nil {
		return
	}
	switch node := instr.(type) {
	case *Assign:
		node.Location = l.location
		if node.Value != nil {
			if location := node.Value.SourceLocation(); location != nil {
				node.Location = location
			}
		}
	case *Store:
		node.Location = l.location
	case *Call:
		node.Location = l.location
	case *InterfaceCall:
		node.Location = l.location
	case *Drop:
		node.Location = l.location
	}
	*out = append(*out, instr)
}

func (l *lowerer) load(out *[]Instr, place *Place, typ ir.TypeID, loc *source.Location) ValueRef {
	name := l.nextTemp()
	l.appendInstr(out, &Assign{Name: name, Value: &Load{Place: place, Type: typ, Location: loc}})
	return &RefName{Name: name, Type: typ, Location: loc}
}

func (l *lowerer) lowerPlace(place *ir.Place, out *[]Instr) *Place {
	if place == nil || place.Root == nil {
		panic("MIR place lowering requires a root expression")
	}
	root := l.lowerExpr(place.Root, out)
	projections := make([]PlaceProjection, 0, len(place.Projections))
	for _, projection := range place.Projections {
		lowered := PlaceProjection{FieldIndex: projection.FieldIndex, Case: projection.Case, Type: projection.Type, Location: projection.Location}
		switch projection.Kind {
		case ir.PlaceProjectionDeref:
			lowered.Kind = PlaceProjectionDeref
		case ir.PlaceProjectionField:
			lowered.Kind = PlaceProjectionField
		case ir.PlaceProjectionIndex:
			lowered.Kind = PlaceProjectionIndex
			lowered.Index = l.lowerExpr(projection.Index, out)
		case ir.PlaceProjectionVariantPayload:
			lowered.Kind = PlaceProjectionVariantPayload
		default:
			panic(fmt.Sprintf("unsupported HIR place projection %d", projection.Kind))
		}
		projections = append(projections, lowered)
	}
	return &Place{
		Root:        root,
		Projections: projections,
		Type:        place.TypeID(),
		Location:    place.Location,
	}
}

func (l *lowerer) setBlockTerm(block *Block, term Terminator) {
	if block == nil || term == nil {
		return
	}
	switch node := term.(type) {
	case *Ret:
		node.Location = l.location
	case *Branch:
		node.Location = l.location
	case *SwitchVariant:
		node.Location = l.location
	case *Jump:
		node.Location = l.location
	}
	block.Term = term
}

func (l *lowerer) lowerExpr(expr ir.Expr, out *[]Instr) ValueRef {
	switch e := expr.(type) {
	case nil:
		return nil
	case *ir.InvalidExpr:
		return &RefConst{Value: "0", Type: ir.InvalidType, Location: e.Origin().Location}
	case *ir.IntLit:
		return &RefConst{Value: e.Value, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.FloatLit:
		return &RefConst{Value: e.Value, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.BoolLit:
		return &RefConst{Value: e.String(), Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.StringLit:
		var name string
		if l.module != nil {
			name = l.module.InternString(e.Value, 1)
		} else {
			name = "@.str.unknown"
		}
		if l.module != nil {
			if typ, ok := l.module.Types.Type(e.TypeID()); ok && typ.Kind == ir.TypeString {
				temp := l.nextTemp()
				l.appendInstr(out, &Assign{Name: temp, Value: &StringLiteral{Name: name, Length: len(e.Value), Type: e.TypeID(), Location: e.Origin().Location}})
				return &RefName{Name: temp, Type: e.TypeID(), Location: e.Origin().Location}
			}
		}
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.ZeroValue:
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &ZeroValue{Type: e.TypeID(), Location: e.Origin().Location}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.VariantMake:
		payload := l.lowerExpr(e.Payload, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &VariantMake{Case: e.Case, Payload: payload, Type: e.TypeID(), Location: e.Origin().Location}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.VariantIs:
		value := l.lowerExpr(e.Value, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &VariantIs{Value: value, Case: e.Case, Type: e.TypeID(), Location: e.Origin().Location}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.Ident:
		return &RefName{Name: e.Name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.Unary:
		arg := l.lowerExpr(e.Arg, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Unary{Op: e.Op, Arg: arg, Type: e.TypeID(), Location: e.Origin().Location}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.Binary:
		left := l.lowerExpr(e.Left, out)
		right := l.lowerExpr(e.Right, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Binary{Op: e.Op, Left: left, Right: right, Type: e.TypeID(), Location: e.Origin().Location}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.Call:
		callee := l.lowerExpr(e.Callee, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &Call{Callee: callee, Args: args, Type: e.TypeID(), Location: e.Origin().Location}
		if l.isVoid(call.Type) {
			l.appendInstr(out, call)
			return nil
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: call})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.Print:
		value := l.lowerExpr(e.Value, out)
		l.appendInstr(out, &Print{Value: value, Newline: e.Newline, Location: e.Origin().Location})
		return nil
	case *ir.Drop:
		value := l.lowerExpr(e.Value, out)
		l.appendInstr(out, &Drop{Value: value, Location: e.Origin().Location})
		return nil
	case *ir.Load:
		place := l.lowerPlace(e.Place, out)
		value := l.load(out, place, e.TypeID(), e.Origin().Location)
		dropRoot := e.DropRoot
		if l.cleanup != nil {
			if _, planned := l.cleanup.ProjectionBase[e.NodeID]; planned {
				dropRoot = true
			}
		}
		if dropRoot {
			l.appendInstr(out, &Drop{Value: place.Root, Location: e.Place.Root.Origin().Location})
		}
		return value
	case *ir.Len:
		value := l.lowerExpr(e.Value, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Len{
			Value: value, Type: e.TypeID(), Location: e.Origin().Location,
		}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.StringChars:
		value := l.lowerExpr(e.Value, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &StringChars{
			Value: value, Type: e.TypeID(), Location: e.Origin().Location,
		}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.AddrOf:
		place := l.lowerPlace(e.Place, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &AddrOf{Place: place, Type: e.TypeID(), Location: e.Origin().Location}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.TempBorrow:
		value := l.lowerExpr(e.Value, out)
		if value == nil {
			panic("temporary borrow requires value expression")
		}
		place := &Place{Root: value, Type: e.Value.TypeID(), Location: e.Value.Origin().Location}
		l.temporaryDrops = append(l.temporaryDrops, value)
		name := l.nextTemp()
		if e.Slice {
			l.appendInstr(out, &Assign{Name: name, Value: &SliceView{
				Source: place,
				Type:   e.TypeID(),
			}})
		} else {
			l.appendInstr(out, &Assign{Name: name, Value: &AddrOf{
				Place: place,
				Type:  e.TypeID(),
			}})
		}
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.SliceView:
		source := l.lowerPlace(e.Source, out)
		var start, end ValueRef
		if e.Start != nil {
			start = l.lowerExpr(e.Start, out)
		}
		if e.End != nil {
			end = l.lowerExpr(e.End, out)
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &SliceView{
			Source:       source,
			Start:        start,
			End:          end,
			EndExclusive: e.EndExclusive,
			Type:         e.TypeID(),
			Location:     e.Origin().Location,
		}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.Field:
		base := l.lowerExpr(e.Base, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Field{Base: base, Index: e.Index, Type: e.TypeID(), Location: e.Origin().Location}})
		dropBase := e.DropBase
		if l.cleanup != nil {
			if _, planned := l.cleanup.ProjectionBase[e.NodeID]; planned {
				dropBase = true
			}
		}
		if dropBase {
			l.appendInstr(out, &Drop{Value: base, Location: e.Base.Origin().Location})
		}
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.StructLit:
		fields := make([]ValueRef, 0, len(e.Fields))
		for _, field := range e.Fields {
			fields = append(fields, l.lowerExpr(field, out))
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &StructLit{Fields: fields, Type: e.TypeID(), Location: e.Origin().Location}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.ArrayLit:
		if e.Dynamic {
			name := l.nextTemp()
			l.appendInstr(out, &Assign{Name: name, Value: &DynamicArrayAlloc{
				Length:   len(e.Values),
				Type:     e.TypeID(),
				Location: e.Origin().Location,
			}})
			array := &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
			arrayType, ok := l.module.Types.Type(e.TypeID())
			if !ok || arrayType.Kind != ir.TypeArray || arrayType.Length != "" {
				panic("dynamic array literal has invalid type")
			}
			for index, valueExpr := range e.Values {
				value := l.lowerExpr(valueExpr, out)
				indexRef := &RefConst{Value: fmt.Sprintf("%d", index), Type: l.module.Types.IndexType(), Location: valueExpr.Origin().Location}
				place := &Place{
					Root: array,
					Projections: []PlaceProjection{{
						Kind: PlaceProjectionIndex, Index: indexRef, Type: arrayType.Elem, Location: valueExpr.Origin().Location,
					}},
					Type: arrayType.Elem, Location: valueExpr.Origin().Location,
				}
				l.appendInstr(out, &Store{Place: place, Value: value, Location: valueExpr.Origin().Location})
			}
			return array
		}
		values := make([]ValueRef, 0, len(e.Values))
		for _, value := range e.Values {
			values = append(values, l.lowerExpr(value, out))
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &ArrayLit{Values: values, Type: e.TypeID(), Location: e.Origin().Location}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.DynamicArrayOp:
		array := l.lowerExpr(e.Array, out)
		var length, value ValueRef
		if e.Length != nil {
			length = l.lowerExpr(e.Length, out)
		}
		if e.Value != nil {
			value = l.lowerExpr(e.Value, out)
		}
		l.appendInstr(out, &DynamicArrayOp{
			Op:        e.Op,
			Array:     array,
			Length:    length,
			Value:     value,
			ArrayType: e.ArrayType,
			Location:  e.Origin().Location,
		})
		return nil
	case *ir.AllocExpr:
		value := l.lowerExpr(e.Value, out)
		var allocRef ValueRef
		if e.Allocator != nil {
			allocRef = l.lowerExpr(e.Allocator, out)
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Alloc{
			Value: value, Allocator: allocRef, Type: e.TypeID(), Location: e.Origin().Location,
		}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.InterfaceMake:
		value := l.lowerExpr(e.Value, out)
		dataType := interfaceDataType(l.module.Types, e.Value.TypeID())

		slots := make([]ValueRef, 0, len(e.Slots))
		for index, slot := range e.Slots {
			wrapperName := ir.InterfaceThunkName(l.module.Types.ABIKey(slot.InterfaceType), l.module.Types.ABIKey(dataType), slot.MethodName, index)
			slot.WrapperName = wrapperName
			slot.DataType = dataType
			l.registerInterfaceThunk(slot)
			slots = append(slots, &RefName{Name: wrapperName, Type: slot.SlotType})
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &InterfaceMake{
			Value:    value,
			DataType: dataType,
			Slots:    slots,
			Type:     e.TypeID(),
			Location: e.Origin().Location,
		}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.InterfaceCall:
		base := l.lowerExpr(e.Base, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &InterfaceCall{Base: base, Slot: e.Slot, Args: args, Consumes: e.Consumes, Type: e.TypeID(), Location: e.Origin().Location}
		if l.isVoid(call.Type) {
			l.appendInstr(out, call)
			return nil
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: call})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.Cast:
		arg := l.lowerExpr(e.Expr, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Cast{Arg: arg, Type: e.TypeID(), Location: e.Origin().Location}})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	default:
		panic(fmt.Sprintf("MIR lowering: unhandled IR expression %T", expr))
	}
}

func (l *lowerer) lowerDiscardedExpr(expr ir.Expr, out *[]Instr) bool {
	if l == nil || out == nil || expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ir.Call:
		callee := l.lowerExpr(e.Callee, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &Call{Callee: callee, Args: args, Type: e.TypeID()}
		l.appendInstr(out, call)
		return true
	case *ir.InterfaceCall:
		base := l.lowerExpr(e.Base, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &InterfaceCall{Base: base, Slot: e.Slot, Args: args, Consumes: e.Consumes, Type: e.TypeID()}
		l.appendInstr(out, call)
		return true
	default:
		return false
	}
}

func interfaceDataType(types *ir.TypeTable, id ir.TypeID) ir.TypeID {
	typ, ok := types.Type(id)
	if !ok {
		return ir.InvalidType
	}
	if typ.Kind == ir.TypeReference || typ.Kind == ir.TypeOwnedPtr {
		return typ.Elem
	}
	return id
}

func (l *lowerer) registerInterfaceThunk(slot ir.InterfaceSlot) {
	if l == nil || l.module == nil || slot.WrapperName == "" {
		return
	}
	for _, w := range l.module.InterfaceThunks {
		if w.Name == slot.WrapperName {
			return
		}
	}
	thunk := &InterfaceThunk{
		Name:     slot.WrapperName,
		SlotType: slot.SlotType,
		FuncName: slot.FuncName,
		FuncType: slot.FuncType,
		DataType: slot.DataType,
	}
	l.module.InterfaceThunks = append(l.module.InterfaceThunks, thunk)
}

func (l *lowerer) nextTemp() string {
	l.tmp++
	return fmt.Sprintf("t%d", l.tmp)
}

func asValueExpr(ref ValueRef) ValueExpr {
	switch node := ref.(type) {
	case *RefConst:
		return &Move{Src: ref, Type: node.Type}
	case *RefName:
		return &Move{Src: ref, Type: node.Type}
	default:
		panic(fmt.Sprintf("MIR lowering: unhandled value reference %T", ref))
	}
}
