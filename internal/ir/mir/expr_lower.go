package mir

import (
	"fmt"

	"compiler/internal/ir"
	"compiler/internal/source"
)

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

func identifierRef(expr *ir.Ident) *RefName {
	return &RefName{Name: expr.Name, Type: expr.TypeID(), Location: expr.Origin().Location}
}

func (l *lowerer) lowerPlace(place *ir.Place, out *[]Instr) *Place {
	if place == nil || place.Root == nil {
		panic("MIR place lowering requires a root expression")
	}
	var root ValueRef
	if ident, ok := place.Root.(*ir.Ident); ok {
		// Places retain original storage; only value reads are captured.
		root = identifierRef(ident)
	} else {
		root = l.lowerExpr(place.Root, out)
	}
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
			panic(fmt.Sprintf("unsupported IR place projection %d", projection.Kind))
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

func (l *lowerer) lowerCall(expr *ir.Call, out *[]Instr) *Call {
	callee := l.lowerExpr(expr.Callee, out)
	args := make([]ValueRef, 0, len(expr.Args))
	for _, arg := range expr.Args {
		args = append(args, l.lowerExpr(arg, out))
	}
	return &Call{Callee: callee, Args: args, Type: expr.TypeID(), Location: expr.Origin().Location}
}

func (l *lowerer) lowerInterfaceCall(expr *ir.InterfaceCall, out *[]Instr) *InterfaceCall {
	base := l.lowerExpr(expr.Base, out)
	args := make([]ValueRef, 0, len(expr.Args))
	for _, arg := range expr.Args {
		args = append(args, l.lowerExpr(arg, out))
	}
	return &InterfaceCall{
		Base:         base,
		Slot:         expr.Slot,
		SlotType:     expr.SlotType,
		Args:         args,
		ConsumesBase: expr.ConsumesBase,
		Type:         expr.TypeID(),
		Location:     expr.Origin().Location,
	}
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
		return l.storeInTemp(out, e, &ZeroValue{Type: e.TypeID(), Location: e.Origin().Location})
	case *ir.VariantMake:
		payload := l.lowerExpr(e.Payload, out)
		return l.storeInTemp(out, e, &VariantMake{Case: e.Case, Payload: payload, Type: e.TypeID(), Location: e.Origin().Location})
	case *ir.VariantIs:
		value := l.lowerExpr(e.Value, out)
		return l.storeInTemp(out, e, &VariantIs{Value: value, Case: e.Case, Type: e.TypeID(), Location: e.Origin().Location})
	case *ir.Ident:
		ref := identifierRef(e)
		// Direct symbols retain linkage names; callable storage is captured too.
		if typ, ok := l.module.Types.Type(e.TypeID()); ok && typ.Kind == ir.TypeFunction && l.symbolValues[e.SymbolID] == nil {
			return ref
		}
		// MIR Move captures a value; semantic consumption remains analysis-owned.
		return l.storeInTemp(out, e, asValueExpr(ref))
	case *ir.Unary:
		arg := l.lowerExpr(e.Arg, out)
		return l.storeInTemp(out, e, &Unary{Op: e.Op, Arg: arg, Type: e.TypeID(), Location: e.Origin().Location})
	case *ir.Binary:
		left := l.lowerExpr(e.Left, out)
		right := l.lowerExpr(e.Right, out)
		return l.storeInTemp(out, e, &Binary{Op: e.Op, Left: left, Right: right, Type: e.TypeID(), Location: e.Origin().Location})
	case *ir.StringConcat:
		left := l.lowerExpr(e.Left, out)
		right := l.lowerExpr(e.Right, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &StringConcat{
			Left: left, Right: right, Type: e.TypeID(), Location: e.Origin().Location,
		}})
		l.appendInstr(out, &Drop{Value: left, Location: e.Left.Origin().Location})
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.Call:
		call := l.lowerCall(e, out)
		if l.isVoid(call.Type) {
			l.appendInstr(out, call)
			return nil
		}
		return l.storeInTemp(out, e, call)
	case *ir.Print:
		value := l.lowerExpr(e.Value, out)
		l.appendInstr(out, &Print{Value: value, AppendsNewline: e.AppendsNewline, Location: e.Origin().Location})
		return nil
	case *ir.Drop:
		value := l.lowerExpr(e.Value, out)
		l.appendInstr(out, &Drop{Value: value, Location: e.Origin().Location})
		return nil
	case *ir.Load:
		place := l.lowerPlace(e.Place, out)
		value := l.load(out, place, e.TypeID(), e.Origin().Location)
		shouldDropRoot := e.DropsRoot
		if l.input.Analysis != nil && l.input.Analysis.ProjectionBase(l.sourceFn.Identity, e.NodeID) {
			shouldDropRoot = true
		}
		if shouldDropRoot {
			l.appendInstr(out, &Drop{Value: place.Root, Location: e.Place.Root.Origin().Location})
		}
		return value
	case *ir.Len:
		value := l.lowerExpr(e.Value, out)
		return l.storeInTemp(out, e, &Len{
			Value: value, Type: e.TypeID(), Location: e.Origin().Location,
		})
	case *ir.StringChars:
		value := l.lowerExpr(e.Value, out)
		return l.storeInTemp(out, e, &StringChars{
			Value: value, Type: e.TypeID(), Location: e.Origin().Location,
		})
	case *ir.StringFromBytes:
		bytes := l.lowerExpr(e.Bytes, out)
		var allocator ValueRef
		if e.Allocator != nil {
			allocator = l.lowerExpr(e.Allocator, out)
		}
		return l.storeInTemp(out, e, &StringFromBytes{
			Bytes: bytes, Allocator: allocator, Type: e.TypeID(), Location: e.Origin().Location,
		})
	case *ir.AddrOf:
		place := l.lowerPlace(e.Place, out)
		return l.storeInTemp(out, e, &AddrOf{Place: place, Type: e.TypeID(), Location: e.Origin().Location})
	case *ir.TempBorrow:
		value := l.lowerExpr(e.Value, out)
		if value == nil {
			panic("temporary borrow requires value expression")
		}
		place := &Place{Root: value, Type: e.Value.TypeID(), Location: e.Value.Origin().Location}
		l.temporaryDrops = append(l.temporaryDrops, value)
		name := l.nextTemp()
		if e.IsSlice {
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
		source := l.lowerPlace(e.Place, out)
		var start, end ValueRef
		if e.Start != nil {
			start = l.lowerExpr(e.Start, out)
		}
		if e.End != nil {
			end = l.lowerExpr(e.End, out)
		}
		return l.storeInTemp(out, e, &SliceView{
			Source:         source,
			Start:          start,
			End:            end,
			IsEndExclusive: e.IsEndExclusive,
			Type:           e.TypeID(),
			Location:       e.Origin().Location,
		})
	case *ir.Field:
		base := l.lowerExpr(e.Base, out)
		name := l.nextTemp()
		l.appendInstr(out, &Assign{Name: name, Value: &Field{Base: base, Index: e.Index, Type: e.TypeID(), Location: e.Origin().Location}})
		shouldDropBase := e.DropsBase
		if l.input.Analysis != nil && l.input.Analysis.ProjectionBase(l.sourceFn.Identity, e.NodeID) {
			shouldDropBase = true
		}
		if shouldDropBase {
			l.appendInstr(out, &Drop{Value: base, Location: e.Base.Origin().Location})
		}
		return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
	case *ir.StructLit:
		fields := make([]ValueRef, 0, len(e.Fields))
		for _, field := range e.Fields {
			fields = append(fields, l.lowerExpr(field, out))
		}
		return l.storeInTemp(out, e, &StructLit{Fields: fields, Type: e.TypeID(), Location: e.Origin().Location})
	case *ir.ArrayLit:
		if e.IsDynamic {
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
		return l.storeInTemp(out, e, &ArrayLit{Values: values, Type: e.TypeID(), Location: e.Origin().Location})
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
		return l.storeInTemp(out, e, &Alloc{
			Value: value, Allocator: allocRef, Type: e.TypeID(), Location: e.Origin().Location,
		})
	case *ir.InterfaceMake:
		value := l.lowerExpr(e.Value, out)
		dataType := interfaceDataType(l.module.Types, e.Value.TypeID())

		slots := make([]ValueRef, 0, len(e.Slots))
		for index, slot := range e.Slots {
			wrapperName := ir.InterfaceThunkName(l.module.Types.ABIKey(slot.InterfaceType), l.module.Types.ABIKey(dataType), slot.MethodName, index)
			slot.WrapperName = wrapperName
			slot.DataType = dataType
			l.registerInterfaceThunk(slot, index)
			slots = append(slots, &RefName{Name: wrapperName, Type: slot.SlotType})
		}
		return l.storeInTemp(out, e, &InterfaceMake{
			Value:    value,
			DataType: dataType,
			Slots:    slots,
			Type:     e.TypeID(),
			Location: e.Origin().Location,
		})
	case *ir.InterfaceCall:
		call := l.lowerInterfaceCall(e, out)
		if l.isVoid(call.Type) {
			l.appendInstr(out, call)
			return nil
		}
		return l.storeInTemp(out, e, call)
	case *ir.Cast:
		arg := l.lowerExpr(e.Expr, out)
		if fields, ok := l.structCastFields(arg, e.TypeID(), e.Origin().Location, out); ok {
			return l.storeInTemp(out, e, &StructLit{Fields: fields, Type: e.TypeID(), Location: e.Origin().Location})
		}
		return l.storeInTemp(out, e, &Cast{Arg: arg, Type: e.TypeID(), Location: e.Origin().Location})
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
		l.appendInstr(out, l.lowerCall(e, out))
		return true
	case *ir.InterfaceCall:
		l.appendInstr(out, l.lowerInterfaceCall(e, out))
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

func (l *lowerer) registerInterfaceThunk(slot ir.InterfaceSlot, index int) {
	if l == nil || l.module == nil || slot.WrapperName == "" {
		return
	}
	for _, w := range l.module.InterfaceThunks {
		if w.Name == slot.WrapperName {
			return
		}
	}
	thunk := &InterfaceThunk{
		Name:          slot.WrapperName,
		InterfaceType: slot.InterfaceType,
		Slot:          index,
		SlotType:      slot.SlotType,
		FuncName:      slot.FuncName,
		FuncType:      slot.FuncType,
		DataType:      slot.DataType,
	}
	l.module.InterfaceThunks = append(l.module.InterfaceThunks, thunk)
}

// storeInTemp binds value to a fresh temporary and returns a reference typed and
// located like e. Callers lower operands first so temporaries stay in
// evaluation order.
func (l *lowerer) storeInTemp(out *[]Instr, e ir.Expr, value ValueExpr) ValueRef {
	name := l.nextTemp()
	l.appendInstr(out, &Assign{Name: name, Value: value})
	return &RefName{Name: name, Type: e.TypeID(), Location: e.Origin().Location}
}

func (l *lowerer) nextTemp() string {
	l.tmp++
	return fmt.Sprintf("t%d", l.tmp)
}

func asValueExpr(ref ValueRef) ValueExpr {
	if ref == nil {
		panic("MIR lowering: nil value reference")
	}
	return &Move{Src: ref}
}
