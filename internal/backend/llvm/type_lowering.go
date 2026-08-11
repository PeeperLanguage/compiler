package llvm

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

type llvmLayoutKind uint8

const (
	llvmLayoutVoid llvmLayoutKind = iota
	llvmLayoutScalar
	llvmLayoutPointer
	llvmLayoutAggregate
	llvmLayoutArray
	llvmLayoutFunction
)

type llvmFieldName string

const (
	llvmFieldData      llvmFieldName = "data"
	llvmFieldLength    llvmFieldName = "length"
	llvmFieldCapacity  llvmFieldName = "capacity"
	llvmFieldAllocator llvmFieldName = "allocator"
	llvmFieldPresent   llvmFieldName = "present"
	llvmFieldValue     llvmFieldName = "value"
	llvmFieldDispatch  llvmFieldName = "dispatch"
)

// llvmLayout is backend-owned physical type evidence. Named carrier fields
// keep ABI field knowledge out of lowering and drop emitters.
type llvmLayout struct {
	Text       string
	Kind       llvmLayoutKind
	Pointee    *llvmLayout
	Element    *llvmLayout
	Elements   []*llvmLayout
	Fields     map[llvmFieldName]int
	Return     *llvmLayout
	Parameters []*llvmLayout
}

func llvmScalarLayout(text string) *llvmLayout {
	return &llvmLayout{Text: text, Kind: llvmLayoutScalar}
}

func llvmPointerLayout(pointee *llvmLayout) *llvmLayout {
	return &llvmLayout{Text: pointee.Text + "*", Kind: llvmLayoutPointer, Pointee: pointee}
}

func llvmAggregateLayout(elements []*llvmLayout, fields map[llvmFieldName]int) *llvmLayout {
	parts := make([]string, len(elements))
	for i, element := range elements {
		parts[i] = element.Text
	}
	return &llvmLayout{Text: "{ " + strings.Join(parts, ", ") + " }", Kind: llvmLayoutAggregate, Elements: elements, Fields: fields}
}

func llvmFunctionLayout(result *llvmLayout, params []*llvmLayout) *llvmLayout {
	parts := make([]string, len(params))
	for i, param := range params {
		parts[i] = param.Text
	}
	return &llvmLayout{
		Text: result.Text + " (" + strings.Join(parts, ", ") + ")*", Kind: llvmLayoutFunction,
		Return: result, Parameters: params,
	}
}

func (e *llvmEmitter) layout(id ir.TypeID) *llvmLayout {
	if e == nil || e.mod == nil || e.mod.Types == nil {
		return nil
	}
	if layout := e.layouts[id]; layout != nil {
		return layout
	}
	layout, ok := llvmLayoutID(e.mod.Types, id)
	if ok {
		if e.layouts == nil {
			e.layouts = make(map[ir.TypeID]*llvmLayout)
		}
		e.layouts[id] = layout
		return layout
	}
	e.invalid = true
	if e.badTypes == nil {
		e.badTypes = make(map[string]struct{})
	}
	text := e.mod.Types.Text(id)
	if _, reported := e.badTypes[text]; !reported {
		e.badTypes[text] = struct{}{}
		if e.diag != nil {
			e.diag.Add(diagnostics.NewError("unsupported llvm type: " + text).WithCode(diagnostics.ErrInvalidType))
		}
	}
	return llvmScalarLayout("i32")
}

func llvmLayoutID(types *ir.TypeTable, id ir.TypeID) (*llvmLayout, bool) {
	typ, ok := types.Type(id)
	if !ok {
		return nil, false
	}
	i8 := llvmScalarLayout("i8")
	rawPointer := llvmPointerLayout(i8)
	switch typ.Kind {
	case ir.TypeVoid:
		return &llvmLayout{Text: "void", Kind: llvmLayoutVoid}, true
	case ir.TypeInteger:
		return llvmScalarLayout(fmt.Sprintf("i%d", typ.Bits)), true
	case ir.TypeFloat:
		switch typ.Bits {
		case 32:
			return llvmScalarLayout("float"), true
		case 64:
			return llvmScalarLayout("double"), true
		default:
			return nil, false
		}
	case ir.TypeBool:
		return llvmScalarLayout("i1"), true
	case ir.TypeByte:
		return i8, true
	case ir.TypeChar:
		return llvmScalarLayout("i32"), true
	case ir.TypeCStr, ir.TypeRawPtr, ir.TypeAllocator:
		return rawPointer, true
	case ir.TypeString:
		index, ok := llvmLayoutID(types, types.IndexType())
		if !ok {
			return nil, false
		}
		return llvmAggregateLayout([]*llvmLayout{rawPointer, index, rawPointer}, map[llvmFieldName]int{
			llvmFieldData: 0, llvmFieldLength: 1, llvmFieldAllocator: 2,
		}), true
	case ir.TypeOwnedPtr:
		if isOwnedInterfaceType(types, id) {
			return llvmAggregateLayout([]*llvmLayout{rawPointer, rawPointer, rawPointer}, map[llvmFieldName]int{
				llvmFieldData: 0, llvmFieldDispatch: 1, llvmFieldAllocator: 2,
			}), true
		}
		elem, ok := llvmLayoutID(types, typ.Elem)
		if !ok {
			return nil, false
		}
		return llvmAggregateLayout([]*llvmLayout{llvmPointerLayout(elem), rawPointer}, map[llvmFieldName]int{
			llvmFieldData: 0, llvmFieldAllocator: 1,
		}), true
	case ir.TypeReference:
		if isInterfaceType(types, typ.Elem) {
			return llvmAggregateLayout([]*llvmLayout{rawPointer, rawPointer}, map[llvmFieldName]int{
				llvmFieldData: 0, llvmFieldDispatch: 1,
			}), true
		}
		elemType, ok := types.Type(typ.Elem)
		if !ok {
			return nil, false
		}
		if elemType.Kind == ir.TypeString {
			index, ok := llvmLayoutID(types, types.IndexType())
			if !ok {
				return nil, false
			}
			return llvmAggregateLayout([]*llvmLayout{rawPointer, index}, map[llvmFieldName]int{
				llvmFieldData: 0, llvmFieldLength: 1,
			}), true
		}
		if elemType.Kind == ir.TypeArray && elemType.Length == "" {
			elem, ok := llvmLayoutID(types, elemType.Elem)
			if !ok {
				return nil, false
			}
			index, ok := llvmLayoutID(types, types.IndexType())
			if !ok {
				return nil, false
			}
			return llvmAggregateLayout([]*llvmLayout{llvmPointerLayout(elem), index}, map[llvmFieldName]int{
				llvmFieldData: 0, llvmFieldLength: 1,
			}), true
		}
		elem, ok := llvmLayoutID(types, typ.Elem)
		if !ok {
			return nil, false
		}
		return llvmPointerLayout(elem), true
	case ir.TypeOptional:
		inner, ok := llvmLayoutID(types, typ.Elem)
		if !ok {
			return nil, false
		}
		return llvmAggregateLayout([]*llvmLayout{llvmScalarLayout("i1"), inner}, map[llvmFieldName]int{
			llvmFieldPresent: 0, llvmFieldValue: 1,
		}), true
	case ir.TypeArray:
		elem, ok := llvmLayoutID(types, typ.Elem)
		if !ok {
			return nil, false
		}
		if typ.Length == "" {
			index, ok := llvmLayoutID(types, types.IndexType())
			if !ok {
				return nil, false
			}
			return llvmAggregateLayout([]*llvmLayout{llvmPointerLayout(elem), index, index, rawPointer}, map[llvmFieldName]int{
				llvmFieldData: 0, llvmFieldLength: 1, llvmFieldCapacity: 2, llvmFieldAllocator: 3,
			}), true
		}
		return &llvmLayout{Text: "[" + typ.Length + " x " + elem.Text + "]", Kind: llvmLayoutArray, Element: elem}, true
	case ir.TypeStruct:
		fields := make([]*llvmLayout, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			llvmField, ok := llvmLayoutID(types, field.Type)
			if !ok {
				return nil, false
			}
			fields = append(fields, llvmField)
		}
		return llvmAggregateLayout(fields, nil), true
	case ir.TypeInterface:
		return llvmAggregateLayout([]*llvmLayout{rawPointer, rawPointer}, map[llvmFieldName]int{
			llvmFieldData: 0, llvmFieldDispatch: 1,
		}), true
	case ir.TypeFunction:
		returnType, ok := llvmLayoutID(types, typ.Return)
		if !ok {
			return nil, false
		}
		params := make([]*llvmLayout, 0, len(typ.Params))
		for _, param := range typ.Params {
			llvmParam, ok := llvmLayoutID(types, param)
			if !ok {
				return nil, false
			}
			params = append(params, llvmParam)
		}
		return llvmFunctionLayout(returnType, params), true
	default:
		return nil, false
	}
}

func isInterfaceType(types *ir.TypeTable, id ir.TypeID) bool {
	typ, ok := types.Type(id)
	return ok && typ.Kind == ir.TypeInterface
}

func isTypeKind(types *ir.TypeTable, id ir.TypeID, kind ir.TypeKind) bool {
	typ, ok := types.Type(id)
	return ok && typ.Kind == kind
}

func isVoidType(types *ir.TypeTable, id ir.TypeID) bool {
	return isTypeKind(types, id, ir.TypeVoid)
}

func isOwnedInterfaceType(types *ir.TypeTable, id ir.TypeID) bool {
	typ, ok := types.Type(id)
	return ok && typ.Kind == ir.TypeOwnedPtr && isInterfaceType(types, typ.Elem)
}

func interfaceTypeID(types *ir.TypeTable, id ir.TypeID) (ir.TypeID, bool) {
	typ, ok := types.Type(id)
	if !ok {
		return ir.InvalidType, false
	}
	if typ.Kind == ir.TypeOwnedPtr || typ.Kind == ir.TypeReference {
		id = typ.Elem
		typ, ok = types.Type(id)
	}
	return id, ok && typ.Kind == ir.TypeInterface
}

func interfaceSlotLLVMLayout(types *ir.TypeTable, id ir.TypeID, slot int) (*llvmLayout, bool) {
	interfaceID, ok := interfaceTypeID(types, id)
	if !ok {
		return nil, false
	}
	iface, _ := types.Type(interfaceID)
	if slot < 0 || slot >= len(iface.Methods) {
		return nil, false
	}
	method := iface.Methods[slot]
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	params := make([]*llvmLayout, 0, len(method.Params))
	params = append(params, rawPointer)
	for index, param := range method.Params {
		if index == 0 {
			continue
		}
		llvmParam, ok := llvmLayoutID(types, param.Type)
		if !ok {
			return nil, false
		}
		params = append(params, llvmParam)
	}
	ret, ok := llvmLayoutID(types, method.Return)
	if !ok {
		return nil, false
	}
	return llvmFunctionLayout(ret, params), true
}

func interfaceMethodVtableSlotID(types *ir.TypeTable, id ir.TypeID, methodSlot int) int {
	offset := interfaceReleaseVtableSlot
	if isOwnedInterfaceType(types, id) {
		offset++
	}
	return offset + methodSlot
}

func dynamicArrayElementType(types *ir.TypeTable, id ir.TypeID) (ir.TypeID, bool) {
	typ, ok := types.Type(id)
	if !ok || typ.Kind != ir.TypeArray || typ.Length != "" {
		return ir.InvalidType, false
	}
	return typ.Elem, true
}

func integerInfoID(types *ir.TypeTable, id ir.TypeID) (signed bool, bits int, ok bool) {
	typ, ok := types.Type(id)
	if !ok {
		return false, 0, false
	}
	if typ.Kind == ir.TypeByte {
		return false, 8, true
	}
	if typ.Kind != ir.TypeInteger {
		return false, 0, false
	}
	return typ.Signed, typ.Bits, true
}

func isUnsignedTypeID(types *ir.TypeTable, id ir.TypeID) bool {
	signed, _, ok := integerInfoID(types, id)
	return ok && !signed
}

func integerComparePredID(types *ir.TypeTable, op string, id ir.TypeID) string {
	if isUnsignedTypeID(types, id) {
		switch op {
		case "<":
			return "ult"
		case "<=":
			return "ule"
		case ">":
			return "ugt"
		case ">=":
			return "uge"
		}
	}
	switch op {
	case "==":
		return "eq"
	case "!=":
		return "ne"
	case "<":
		return "slt"
	case "<=":
		return "sle"
	case ">":
		return "sgt"
	case ">=":
		return "sge"
	default:
		return "eq"
	}
}

func (e *llvmEmitter) markInvalid(msg string) {
	if e == nil {
		return
	}
	e.invalid = true
	if e.diag != nil {
		e.diag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrInvalidType))
	}
}

func interfaceSymbolName(prefix string, types *ir.TypeTable, interfaceType, dataType ir.TypeID) string {
	kind := "borrowed"
	if isOwnedInterfaceType(types, interfaceType) {
		kind = "owned"
	}
	raw := fmt.Sprintf("__%s__%s__%s__%s", prefix, kind, types.ABIKey(interfaceType), types.ABIKey(dataType))
	return "@" + ir.SanitizeSymbolName(raw)
}

const interfaceReleaseVtableSlot = 1

func interfaceVtableLength(types *ir.TypeTable, interfaceType ir.TypeID, methodCount int) int {
	if isOwnedInterfaceType(types, interfaceType) {
		return methodCount + 2
	}
	return methodCount + 1
}

func mirValueType(expr mir.ValueExpr) ir.TypeID {
	switch v := expr.(type) {
	case *mir.Move:
		return mirRefType(v.Src)
	case *mir.Unary:
		return v.Type
	case *mir.Binary:
		return v.Type
	case *mir.Cast:
		return v.Type
	case *mir.AddrOf:
		return v.Type
	case *mir.SliceView:
		return v.Type
	case *mir.Load:
		return v.Type
	case *mir.Len:
		return v.Type
	case *mir.StringChars:
		return v.Type
	case *mir.Field:
		return v.Type
	case *mir.StructLit:
		return v.Type
	case *mir.ArrayLit:
		return v.Type
	case *mir.DynamicArrayAlloc:
		return v.Type
	case *mir.DynamicArrayOp:
		return v.Type
	case *mir.ZeroValue:
		return v.Type
	case *mir.OptionalSome:
		return v.Type
	case *mir.InterfaceMake:
		return v.Type
	case *mir.InterfaceCall:
		return v.Type
	case *mir.StringLiteral:
		return v.Type
	case *mir.Call:
		return v.Type
	default:
		return ir.InvalidType
	}
}
