package mir

import (
	"fmt"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

type lowerer struct {
	module         *Module
	fn             *Function
	tmp            int
	nextBlockID    int
	current        *Block
	location       *source.Location
	temporaryDrops []ValueRef
	cleanup        *cfg.CleanupPlan
	symbolValues   map[symbols.SymbolID]*RefName
}

func (l *lowerer) isVoid(id ir.TypeID) bool {
	if l == nil || l.module == nil || l.module.Types == nil {
		return false
	}
	typ, ok := l.module.Types.Type(id)
	return ok && typ.Kind == ir.TypeVoid
}

func GenerateMIR(in *hir.Module, graphs []*cfg.Graph, scope *table.Scope, constValues map[symbols.SymbolID]constvalue.Value) *Module {
	if in == nil {
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
				entry, ok := staticEntryForConst(in.Types, sym, constValues[sym.ID])
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
	graphForFunction := make(map[*hir.Function]*cfg.Graph, len(graphs))
	for _, graph := range graphs {
		if graph != nil && graph.Source != nil {
			graphForFunction[graph.Source] = graph
		}
	}
	for _, hirFn := range in.Funcs {
		graph := graphForFunction[hirFn]
		if graph == nil {
			return nil
		}
		fn, ok := lowerCFGFunction(out, graph)
		if !ok {
			return nil
		}
		out.Funcs = append(out.Funcs, fn)
	}
	return out
}

// lowerCFGFunction converts one normalized CFG into MIR without rebuilding
// branches or loops from structured HIR.
func lowerCFGFunction(mod *Module, graph *cfg.Graph) (*Function, bool) {
	if mod == nil || graph == nil || graph.Source == nil || graph.Entry == nil {
		return nil, false
	}
	fn := &Function{
		Name:       graph.Name,
		Params:     append([]ir.Param(nil), graph.Source.Params...),
		ReturnType: graph.ReturnType,
		Blocks:     make([]*Block, 0, len(graph.Blocks)),
		Location:   graph.Source.Location,
	}
	l := &lowerer{module: mod, fn: fn, cleanup: graph.Cleanup, symbolValues: make(map[symbols.SymbolID]*RefName)}
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
		for _, stmt := range source.Stmts {
			if binding, ok := stmt.(*hir.Binding); ok && binding.SymbolID != 0 {
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
		block := blocks[source]
		if block == nil {
			continue
		}
		l.current = block
		l.location = source.Location
		for _, stmt := range source.Stmts {
			if !l.lowerCFGStmt(stmt) {
				return nil, false
			}
		}
		if l.cleanup != nil {
			for _, scopeID := range source.ScopeExits {
				l.appendPlannedDrops(l.cleanup.AfterScope[scopeID], &block.Instrs)
			}
		}
		if !l.lowerCFGTerminator(source, graph.Exit, blocks) {
			return nil, false
		}
	}
	return fn, true
}

func staticEntryForConst(types *ir.TypeTable, sym *symbols.Symbol, value constvalue.Value) (*StaticEntry, bool) {
	if types == nil || sym == nil || value == nil {
		return nil, false
	}
	valueText, ok := constStaticValueText(value)
	if !ok {
		return nil, false
	}
	typeText := value.TypeText()
	if sym.Type != nil {
		typeText = typeinfo.TypeText(typeinfo.Underlying(sym.Type))
	}
	typ, ok := types.LookupText(typeText)
	if !ok {
		return nil, false
	}
	align := 4
	if typeText == "cstr" {
		align = 8
	}
	return &StaticEntry{
		Name:  fmt.Sprintf("@%s$%d", sym.Name, sym.ID),
		Type:  typ,
		Value: valueText,
		Align: align,
	}, true
}

func constStaticValueText(value constvalue.Value) (string, bool) {
	switch v := value.(type) {
	case *constvalue.IntConst:
		if v == nil {
			return "", false
		}
		return v.Text(), true
	case *constvalue.FloatConst:
		if v == nil {
			return "", false
		}
		return llvmFloatConstText(v.Text()), true
	case *constvalue.BoolConst:
		if v == nil {
			return "", false
		}
		if v.Bool() {
			return "true", true
		}
		return "false", true
	case *constvalue.StringConst:
		if v == nil {
			return "", false
		}
		return v.Text(), true
	default:
		return "", false
	}
}

func llvmFloatConstText(value string) string {
	if strings.ContainsAny(value, ".eE") {
		return value
	}
	return value + ".0"
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
		return false
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

func (l *lowerer) lowerCFGTerminator(source, exit *cfg.Block, blocks map[*cfg.Block]*Block) bool {
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
		temporaryMark := len(l.temporaryDrops)
		cond := l.lowerExpr(term.Cond, &l.current.Instrs)
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		l.setBlockTerm(l.current, &Branch{Cond: cond, ThenID: thenBlock.ID, ElseID: elseBlock.ID})
		return true
	case *cfg.Return:
		temporaryMark := len(l.temporaryDrops)
		value := l.lowerExpr(term.Value, &l.current.Instrs)
		l.flushTemporaryDrops(&l.current.Instrs, temporaryMark)
		for _, stmt := range source.Stmts {
			if ret, ok := stmt.(*hir.Return); ok {
				for _, cleanup := range ret.Cleanup {
					l.lowerExpr(cleanup, &l.current.Instrs)
				}
			}
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
		return false
	}
}

func (l *lowerer) appendInstr(out *[]Instr, instr Instr) {
	if out == nil || instr == nil {
		return
	}
	switch node := instr.(type) {
	case *Assign:
		node.Location = l.location
		if exprLoc := ValueExprLocation(node.Value); exprLoc != nil {
			node.Location = exprLoc
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
		lowered := PlaceProjection{FieldIndex: projection.FieldIndex, Type: projection.Type, Location: projection.Location}
		switch projection.Kind {
		case ir.PlaceProjectionDeref:
			lowered.Kind = PlaceProjectionDeref
		case ir.PlaceProjectionField:
			lowered.Kind = PlaceProjectionField
		case ir.PlaceProjectionIndex:
			lowered.Kind = PlaceProjectionIndex
			lowered.Index = l.lowerExpr(projection.Index, out)
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
	case *Jump:
		node.Location = l.location
	}
	block.Term = term
}

func (l *lowerer) lowerExpr(expr ir.Expr, out *[]Instr) ValueRef {
	switch e := expr.(type) {
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
	case *ir.OptionalSome:
		value := l.lowerExpr(e.Value, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &OptionalSome{Value: value, Type: e.TypeID(), Location: e.Origin().Location}})
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
		return &RefConst{Value: "0", Type: ir.InvalidType}
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
		return &Move{Src: ref, Type: ir.InvalidType}
	}
}
