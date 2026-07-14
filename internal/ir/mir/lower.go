package mir

import (
	"fmt"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

type lowerer struct {
	module      *Module
	fn          *Function
	tmp         int
	nextBlockID int
	current     *Block
	location    *source.Location
}

func GenerateMIR(in *hir.Module, scope *table.Scope, constValues map[symbols.SymbolID]constvalue.Value) *Module {
	if in == nil {
		return nil
	}
	out := &Module{
		FilePath:        in.FilePath,
		Name:            in.Name,
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
				entry, ok := staticEntryForConst(sym, constValues[sym.ID])
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
		if hirFn == nil {
			continue
		}
		fn := &Function{
			Name:       hirFn.Name,
			Params:     append([]ir.Param(nil), hirFn.Params...),
			ReturnType: hirFn.ReturnType,
			EntryID:    0,
			Blocks:     make([]*Block, 0),
			Location:   hirFn.Location,
		}
		l := &lowerer{module: out, fn: fn}
		l.current = l.newBlock()
		fn.EntryID = l.current.ID
		if !l.appendBlock(hirFn.Body) {
			return nil
		}
		if l.current != nil && l.current.Term == nil && fn.ReturnType == "void" {
			l.setBlockTerm(l.current, &Ret{})
			l.current = nil
		}
		out.Funcs = append(out.Funcs, fn)
	}
	return out
}

func staticEntryForConst(sym *symbols.Symbol, value constvalue.Value) (*StaticEntry, bool) {
	if sym == nil || value == nil {
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
	align := 4
	if typeText == "cstr" {
		align = 8
	}
	return &StaticEntry{
		Name:  fmt.Sprintf("@%s$%d", sym.Name, sym.ID),
		Type:  typeText,
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
		return v.Value, true
	case *constvalue.FloatConst:
		if v == nil {
			return "", false
		}
		return llvmFloatConstText(v.Value), true
	case *constvalue.BoolConst:
		if v == nil {
			return "", false
		}
		if v.Value {
			return "true", true
		}
		return "false", true
	case *constvalue.StringConst:
		if v == nil {
			return "", false
		}
		return v.Value, true
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

func (l *lowerer) newBlock() *Block {
	block := &Block{
		ID:     l.nextBlockID,
		Instrs: make([]Instr, 0),
	}
	l.nextBlockID++
	l.fn.Blocks = append(l.fn.Blocks, block)
	return block
}

func (l *lowerer) appendBlock(block *hir.Block) bool {
	if block == nil {
		return true
	}
	for _, stmt := range block.Stmts {
		if !l.appendStmt(stmt) {
			return false
		}
		if l.current == nil {
			break
		}
	}
	return true
}

func (l *lowerer) appendStmt(stmt hir.Stmt) bool {
	if l == nil || stmt == nil {
		return true
	}
	prevLoc := l.location
	l.location = hir.LocOf(stmt)
	defer func() {
		l.location = prevLoc
	}()
	switch node := stmt.(type) {
	case *hir.Block:
		return l.appendBlock(node)
	case *hir.Binding:
		if l.current == nil {
			return true
		}
		ref := l.lowerExpr(node.Value, &l.current.Instrs)
		if refName, ok := ref.(*RefName); ok && refName.Name == node.Name {
			return true
		}
		l.appendInstr(&l.current.Instrs, &Assign{Name: node.Name, Value: asValueExpr(ref)})
		return true
	case *hir.Return:
		if l.current == nil {
			return true
		}
		retRef := l.lowerExpr(node.Value, &l.current.Instrs)
		for _, cleanup := range node.Cleanup {
			l.lowerExpr(cleanup, &l.current.Instrs)
		}
		l.setBlockTerm(l.current, &Ret{Value: retRef})
		l.current = nil
		return true
	case *hir.ExprStmt:
		if l.current == nil {
			return true
		}
		if l.lowerDiscardedExpr(node.Value, &l.current.Instrs) {
			return true
		}
		l.lowerExpr(node.Value, &l.current.Instrs)
		return true
	case *hir.Assign:
		if l.current == nil {
			return true
		}
		value := l.lowerExpr(node.Value, &l.current.Instrs)
		target := node.Target
		if target == nil || target.Root == nil {
			return false
		}
		if ident, direct := target.Root.(*ir.Ident); direct && len(target.Projections) == 0 {
			if node.DropTarget {
				l.appendInstr(&l.current.Instrs, &Drop{Value: &RefName{Name: ident.Name, Type: target.TypeText(), Location: ir.ExprLocation(ident)}})
			}
			l.appendInstr(&l.current.Instrs, &Assign{Name: ident.Name, Value: asValueExpr(value)})
			return true
		}
		place := l.lowerPlace(target, &l.current.Instrs)
		if node.DropTarget {
			l.appendInstr(&l.current.Instrs, &Drop{Value: l.load(&l.current.Instrs, place, target.TypeText(), target.Location)})
		}
		l.appendInstr(&l.current.Instrs, &Store{Place: place, Value: value})
		return true
	case *hir.If:
		return l.appendIf(node)
	case *hir.For:
		return l.appendFor(node)
	default:
		return false
	}
}

func (l *lowerer) appendIf(node *hir.If) bool {
	if l.current == nil || node == nil {
		return true
	}
	condRef := l.lowerExpr(node.Cond, &l.current.Instrs)
	condBlock := l.current
	thenBlock := l.newBlock()
	elseBlock := l.newBlock()
	l.setBlockTerm(condBlock, &Branch{Cond: condRef, ThenID: thenBlock.ID, ElseID: elseBlock.ID})

	l.current = thenBlock
	if !l.appendBlock(node.Then) {
		return false
	}
	thenFall := l.current

	l.current = elseBlock
	if node.Else != nil {
		if !l.appendStmt(node.Else) {
			return false
		}
	}
	elseFall := l.current

	if thenFall == nil && elseFall == nil {
		l.current = nil
		return true
	}

	join := l.newBlock()
	if thenFall != nil && thenFall.Term == nil {
		l.setBlockTerm(thenFall, &Jump{TargetID: join.ID})
	}
	if elseFall != nil && elseFall.Term == nil {
		l.setBlockTerm(elseFall, &Jump{TargetID: join.ID})
	}
	l.current = join
	return true
}

func (l *lowerer) appendFor(node *hir.For) bool {
	if l.current == nil || node == nil || node.Body == nil {
		return true
	}
	if node.Cond == nil {
		bodyBlock := l.newBlock()
		l.setBlockTerm(l.current, &Jump{TargetID: bodyBlock.ID})
		l.current = bodyBlock
		if !l.appendBlock(node.Body) {
			return false
		}
		if l.current != nil && l.current.Term == nil {
			l.setBlockTerm(l.current, &Jump{TargetID: bodyBlock.ID})
		}
		l.current = nil
		return true
	}
	headerBlock := l.newBlock()
	bodyBlock := l.newBlock()
	exitBlock := l.newBlock()
	l.setBlockTerm(l.current, &Jump{TargetID: headerBlock.ID})

	l.current = headerBlock
	condRef := l.lowerExpr(node.Cond, &l.current.Instrs)
	l.setBlockTerm(headerBlock, &Branch{Cond: condRef, ThenID: bodyBlock.ID, ElseID: exitBlock.ID})

	l.current = bodyBlock
	if !l.appendBlock(node.Body) {
		return false
	}
	if l.current != nil && l.current.Term == nil {
		l.setBlockTerm(l.current, &Jump{TargetID: headerBlock.ID})
	}
	l.current = exitBlock
	return true
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

func (l *lowerer) load(out *[]Instr, place *Place, typ string, loc *source.Location) ValueRef {
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
		Type:        place.TypeText(),
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
		return &RefConst{Value: e.Value, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.FloatLit:
		return &RefConst{Value: e.Value, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.BoolLit:
		return &RefConst{Value: e.String(), Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.StringLit:
		var name string
		if l.module != nil {
			elemType := fmt.Sprintf("[%d x i8]", len(e.Value)+1)
			name = l.module.InternStatic(e.Value, elemType, 1)
		} else {
			name = "@.str.unknown"
		}
		return &RefName{Name: name, Type: "cstr", Location: ir.ExprLocation(e)}
	case *ir.ZeroValue:
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &ZeroValue{Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.OptionalSome:
		value := l.lowerExpr(e.Value, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &OptionalSome{Value: value, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Ident:
		return &RefName{Name: e.Name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Unary:
		arg := l.lowerExpr(e.Arg, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Unary{Op: e.Op, Arg: arg, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Binary:
		left := l.lowerExpr(e.Left, out)
		right := l.lowerExpr(e.Right, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Binary{Op: e.Op, Left: left, Right: right, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Call:
		callee := l.lowerExpr(e.Callee, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &Call{Callee: callee, Args: args, Type: e.TypeText(), Location: ir.ExprLocation(e)}
		if call.Type == "" {
			call.Type = "void"
			l.appendInstr(out, call)
			return nil
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: call})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Print:
		value := l.lowerExpr(e.Value, out)
		l.appendInstr(out, &Print{Value: value, Location: ir.ExprLocation(e)})
		return nil
	case *ir.Drop:
		value := l.lowerExpr(e.Value, out)
		l.appendInstr(out, &Drop{Value: value, Location: ir.ExprLocation(e)})
		return nil
	case *ir.Load:
		place := l.lowerPlace(e.Place, out)
		value := l.load(out, place, e.TypeText(), ir.ExprLocation(e))
		if e.DropRoot {
			l.appendInstr(out, &Drop{Value: place.Root, Location: ir.ExprLocation(e.Place.Root)})
		}
		return value
	case *ir.AddrOf:
		pointerType := e.TypeText()
		if pointerType == "rawptr" {
			pointerType = "&mut " + e.Place.TypeText()
		}
		place := l.lowerPlace(e.Place, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &AddrOf{Place: place, Type: pointerType, Location: ir.ExprLocation(e)}})
		address := &RefName{Name: name, Type: pointerType, Location: ir.ExprLocation(e)}
		if e.TypeText() == "rawptr" {
			castName := l.nextTemp()
			l.appendInstr(out, &Assign{Name: castName, Value: &Cast{Arg: address, Type: "rawptr", Location: ir.ExprLocation(e)}})
			return &RefName{Name: castName, Type: "rawptr", Location: ir.ExprLocation(e)}
		}
		return address
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
			Type:         e.TypeText(),
			Location:     ir.ExprLocation(e),
		}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Field:
		base := l.lowerExpr(e.Base, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Field{Base: base, Index: e.Index, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		if e.DropBase {
			l.appendInstr(out, &Drop{Value: base, Location: ir.ExprLocation(e.Base)})
		}
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.StructLit:
		fields := make([]ValueRef, 0, len(e.Fields))
		for _, field := range e.Fields {
			fields = append(fields, l.lowerExpr(field, out))
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &StructLit{Fields: fields, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.ArrayLit:
		if e.Dynamic {
			name := l.nextTemp()
			l.appendInstr(out, &Assign{Name: name, Value: &DynamicArrayAlloc{
				Length:   len(e.Values),
				Type:     e.TypeText(),
				Location: ir.ExprLocation(e),
			}})
			array := &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
			elemType, ok := strings.CutPrefix(e.TypeText(), "[]")
			if !ok || strings.TrimSpace(elemType) == "" {
				panic(fmt.Sprintf("dynamic array literal has invalid type %q", e.TypeText()))
			}
			elemType = strings.TrimSpace(elemType)
			for index, valueExpr := range e.Values {
				value := l.lowerExpr(valueExpr, out)
				indexRef := &RefConst{Value: fmt.Sprintf("%d", index), Type: "usize", Location: ir.ExprLocation(valueExpr)}
				place := &Place{
					Root: array,
					Projections: []PlaceProjection{{
						Kind: PlaceProjectionIndex, Index: indexRef, Type: elemType, Location: ir.ExprLocation(valueExpr),
					}},
					Type: elemType, Location: ir.ExprLocation(valueExpr),
				}
				l.appendInstr(out, &Store{Place: place, Value: value, Location: ir.ExprLocation(valueExpr)})
			}
			return array
		}
		values := make([]ValueRef, 0, len(e.Values))
		for _, value := range e.Values {
			values = append(values, l.lowerExpr(value, out))
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &ArrayLit{Values: values, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.DynamicArrayOp:
		array := l.lowerExpr(e.Array, out)
		var length, value ValueRef
		if e.Length != nil {
			length = l.lowerExpr(e.Length, out)
		}
		if e.Value != nil {
			value = l.lowerExpr(e.Value, out)
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &DynamicArrayOp{
			Op:       e.Op,
			Array:    array,
			Length:   length,
			Value:    value,
			Type:     e.TypeText(),
			Location: ir.ExprLocation(e),
		}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.InterfaceMake:
		value := l.lowerExpr(e.Value, out)
		dataType := interfaceDataType(e.Value.TypeText())

		slots := make([]ValueRef, 0, len(e.Slots))
		for index, slot := range e.Slots {
			wrapperName := ir.InterfaceThunkName(slot.InterfaceType, dataType, slot.MethodName, index)
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
			Type:     e.TypeText(),
			Location: ir.ExprLocation(e),
		}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.InterfaceCall:
		base := l.lowerExpr(e.Base, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &InterfaceCall{Base: base, Slot: e.Slot, Args: args, Consumes: e.Consumes, Type: e.TypeText(), Location: ir.ExprLocation(e)}
		if call.Type == "" {
			call.Type = "void"
			l.appendInstr(out, call)
			return nil
		}
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: call})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	case *ir.Cast:
		arg := l.lowerExpr(e.Expr, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Cast{Arg: arg, Type: e.TypeText(), Location: ir.ExprLocation(e)}})
		return &RefName{Name: name, Type: e.TypeText(), Location: ir.ExprLocation(e)}
	default:
		return &RefConst{Value: "0", Type: "i32"}
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
		call := &Call{Callee: callee, Args: args, Type: e.TypeText()}
		if call.Type == "" {
			call.Type = "void"
		}
		l.appendInstr(out, call)
		return true
	case *ir.InterfaceCall:
		base := l.lowerExpr(e.Base, out)
		args := make([]ValueRef, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, l.lowerExpr(arg, out))
		}
		call := &InterfaceCall{Base: base, Slot: e.Slot, Args: args, Consumes: e.Consumes, Type: e.TypeText()}
		if call.Type == "" {
			call.Type = "void"
		}
		l.appendInstr(out, call)
		return true
	default:
		return false
	}
}

func interfaceDataType(typeText string) string {
	if remainder, ok := strings.CutPrefix(typeText, "&mut "); ok {
		return remainder
	}
	if remainder, ok := strings.CutPrefix(typeText, "&"); ok {
		return remainder
	}
	if remainder, ok := strings.CutPrefix(typeText, "*"); ok {
		return remainder
	}
	return typeText
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
		return &Move{Src: ref, Type: "i32"}
	}
}
