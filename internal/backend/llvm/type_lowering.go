package llvm

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

func (e *llvmEmitter) llvmType(id ir.TypeID) string {
	if mapped, ok := llvmTypeID(e.mod.Types, id); ok {
		return mapped
	}
	if e != nil {
		e.invalid = true
		if e.badTypes == nil {
			e.badTypes = make(map[string]struct{})
		}
		text := "<invalid>"
		if e.mod != nil && e.mod.Types != nil {
			text = e.mod.Types.Text(id)
		}
		if _, ok := e.badTypes[text]; !ok {
			e.badTypes[text] = struct{}{}
			if e.diag != nil {
				msg := "unsupported llvm type"
				if text != "<invalid>" {
					msg = msg + ": " + text
				}
				e.diag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrInvalidType))
			}
		}
	}
	return "i32"
}

// llvmTypeID lowers canonical compiler layout descriptors. LLVM owns physical
// ABI sizing, while the table owns carrier shape and field order.
func llvmTypeID(types *ir.TypeTable, id ir.TypeID) (string, bool) {
	typ, ok := types.Type(id)
	if !ok {
		return "", false
	}
	switch typ.Kind {
	case ir.TypeVoid:
		return "void", true
	case ir.TypeInteger:
		return fmt.Sprintf("i%d", typ.Bits), true
	case ir.TypeFloat:
		switch typ.Bits {
		case 32:
			return "float", true
		case 64:
			return "double", true
		default:
			return "", false
		}
	case ir.TypeBool:
		return "i1", true
	case ir.TypeByte:
		return "i8", true
	case ir.TypeChar:
		return "i32", true
	case ir.TypeCStr, ir.TypeRawPtr, ir.TypeAllocator:
		return "i8*", true
	case ir.TypeString:
		index, ok := llvmTypeID(types, types.IndexType())
		if !ok {
			return "", false
		}
		return "{ i8*, " + index + ", i8* }", true
	case ir.TypeOwnedPtr:
		if isOwnedInterfaceType(types, id) {
			return "{ i8*, i8*, i8* }", true
		}
		elem, ok := llvmTypeID(types, typ.Elem)
		if !ok {
			return "", false
		}
		return "{ " + elem + "*, i8* }", true
	case ir.TypeReference:
		if isInterfaceType(types, typ.Elem) {
			return "{ i8*, i8* }", true
		}
		elemType, ok := types.Type(typ.Elem)
		if !ok {
			return "", false
		}
		if elemType.Kind == ir.TypeString {
			index, ok := llvmTypeID(types, types.IndexType())
			if !ok {
				return "", false
			}
			return "{ i8*, " + index + " }", true
		}
		if elemType.Kind == ir.TypeArray && elemType.Length == "" {
			elem, ok := llvmTypeID(types, elemType.Elem)
			if !ok {
				return "", false
			}
			index, ok := llvmTypeID(types, types.IndexType())
			if !ok {
				return "", false
			}
			return "{ " + elem + "*, " + index + " }", true
		}
		elem, ok := llvmTypeID(types, typ.Elem)
		if !ok {
			return "", false
		}
		return elem + "*", true
	case ir.TypeOptional:
		inner, ok := llvmTypeID(types, typ.Elem)
		if !ok {
			return "", false
		}
		return "{ i1, " + inner + " }", true
	case ir.TypeArray:
		elem, ok := llvmTypeID(types, typ.Elem)
		if !ok {
			return "", false
		}
		if typ.Length == "" {
			index, ok := llvmTypeID(types, types.IndexType())
			if !ok {
				return "", false
			}
			return "{ " + elem + "*, " + index + ", " + index + ", i8* }", true
		}
		return "[" + typ.Length + " x " + elem + "]", true
	case ir.TypeStruct:
		fields := make([]string, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			llvmField, ok := llvmTypeID(types, field.Type)
			if !ok {
				return "", false
			}
			fields = append(fields, llvmField)
		}
		return "{ " + strings.Join(fields, ", ") + " }", true
	case ir.TypeInterface:
		return "{ i8*, i8* }", true
	case ir.TypeFunction:
		returnType, ok := llvmTypeID(types, typ.Return)
		if !ok {
			return "", false
		}
		params := make([]string, 0, len(typ.Params))
		for _, param := range typ.Params {
			llvmParam, ok := llvmTypeID(types, param)
			if !ok {
				return "", false
			}
			params = append(params, llvmParam)
		}
		return returnType + " (" + strings.Join(params, ", ") + ")*", true
	default:
		return "", false
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

func interfaceSlotLLVMType(types *ir.TypeTable, id ir.TypeID, slot int) (string, bool) {
	interfaceID, ok := interfaceTypeID(types, id)
	if !ok {
		return "", false
	}
	iface, _ := types.Type(interfaceID)
	if slot < 0 || slot >= len(iface.Methods) {
		return "", false
	}
	method := iface.Methods[slot]
	params := make([]string, 0, len(method.Params))
	params = append(params, "i8*")
	for index, param := range method.Params {
		if index == 0 {
			continue
		}
		llvmParam, ok := llvmTypeID(types, param.Type)
		if !ok {
			return "", false
		}
		params = append(params, llvmParam)
	}
	ret, ok := llvmTypeID(types, method.Return)
	if !ok {
		return "", false
	}
	return ret + " (" + strings.Join(params, ", ") + ")*", true
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

func llvmFunctionSignature(types *ir.TypeTable, id ir.TypeID) (string, []string, bool) {
	typ, ok := types.Type(id)
	if !ok || typ.Kind != ir.TypeFunction {
		return "", nil, false
	}
	ret, ok := llvmTypeID(types, typ.Return)
	if !ok {
		return "", nil, false
	}
	params := make([]string, 0, len(typ.Params))
	for _, param := range typ.Params {
		llvmParam, ok := llvmTypeID(types, param)
		if !ok {
			return "", nil, false
		}
		params = append(params, llvmParam)
	}
	return ret, params, true
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
