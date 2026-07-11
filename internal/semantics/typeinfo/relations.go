package typeinfo

import "compiler/internal/frontend/token"

func SameType(left, right Type) bool {
	if left == right {
		return true
	}
	left = Underlying(left)
	right = Underlying(right)
	switch l := left.(type) {
	case *InvalidType:
		_, ok := right.(*InvalidType)
		return ok
	case *UnknownType:
		_, ok := right.(*UnknownType)
		return ok
	case *IntegerType:
		r, ok := right.(*IntegerType)
		return ok && r != nil && l.Signed == r.Signed && l.Bits == r.Bits
	case *ByteType:
		_, ok := right.(*ByteType)
		return ok
	case *BoolType:
		_, ok := right.(*BoolType)
		return ok
	case *CStrType:
		_, ok := right.(*CStrType)
		return ok
	case *StringType:
		_, ok := right.(*StringType)
		return ok
	case *NoneType:
		_, ok := right.(*NoneType)
		return ok
	case *FloatType:
		r, ok := right.(*FloatType)
		return ok && r != nil && l.Bits == r.Bits
	case *NamedType:
		r, ok := right.(*NamedType)
		return ok && r != nil && l.Name == r.Name
	case *OwnedPtrType:
		r, ok := right.(*OwnedPtrType)
		return ok && r != nil && SameType(l.Target, r.Target)
	case *RawPtrType:
		return checkPointerCompatibility(l, right) == Compatible
	case *RefType:
		r, ok := right.(*RefType)
		return ok && r != nil && l.Mutable == r.Mutable && SameType(l.Target, r.Target)
	case *OptionalType:
		r, ok := right.(*OptionalType)
		return ok && r != nil && SameType(l.Inner, r.Inner)
	case *ArrayType:
		r, ok := right.(*ArrayType)
		return ok && r != nil && l.Len == r.Len && l.Dynamic == r.Dynamic && SameType(l.Elem, r.Elem)
	case *FuncType:
		return checkFuncCompatibility(l, right) == Compatible
	case *StructType:
		return checkStructCompatibility(l, right) == Compatible
	case *InterfaceType:
		return checkInterfaceCompatibility(l, right) == Compatible
	case *EnumType:
		return checkEnumCompatibility(l, right) == Compatible
	default:
		return left == nil && right == nil
	}
}

type NumericFamily int

const (
	NumericInvalid NumericFamily = iota
	NumericSigned
	NumericUnsigned
	NumericByte
	NumericFloat
)

func NumericInfo(t Type) (family NumericFamily, bits int, ok bool) {
	t = Underlying(t)
	switch typ := t.(type) {
	case *IntegerType:
		if typ == nil {
			return NumericInvalid, 0, false
		}
		if typ.Signed {
			return NumericSigned, typ.Bits, true
		}
		return NumericUnsigned, typ.Bits, true
	case *ByteType:
		return NumericByte, 8, true
	case *FloatType:
		if typ == nil {
			return NumericInvalid, 0, false
		}
		return NumericFloat, typ.Bits, true
	case *NamedType:
		if typ == nil {
			return NumericInvalid, 0, false
		}
		if typ.Name == "byte" {
			return NumericByte, 8, true
		}
		if signed, bits, ok := token.ParseIntegerBuiltin(typ.Name); ok {
			if signed {
				return NumericSigned, bits, true
			}
			return NumericUnsigned, bits, true
		}
		switch typ.Name {
		case "f32":
			return NumericFloat, 32, true
		case "f64":
			return NumericFloat, 64, true
		default:
			return NumericInvalid, 0, false
		}
	default:
		return NumericInvalid, 0, false
	}
}

// NumericTypeFromName is the canonical bridge from explicit source type text
// to semantic numeric identity. Arbitrary float widths stay rejected until the
// language has a representation independent from LLVM's target float set.
func NumericTypeFromName(name string) (Type, bool) {
	if signed, bits, ok := token.ParseIntegerBuiltin(name); ok {
		return &IntegerType{Signed: signed, Bits: bits}, true
	}
	switch name {
	case "f32":
		return &FloatType{Bits: 32}, true
	case "f64":
		return &FloatType{Bits: 64}, true
	default:
		return nil, false
	}
}

func CommonNumericType(a, b Type) Type {
	if _, _, ok := NumericInfo(a); !ok {
		return nil
	}
	if _, _, ok := NumericInfo(b); !ok {
		return nil
	}
	if SameType(a, b) {
		return a
	}
	if CheckNumericCompatibility(a, b) == Compatible {
		return a
	}
	if CheckNumericCompatibility(b, a) == Compatible {
		return b
	}
	return nil
}

func Assignable(dst, src Type) bool {
	return CheckCompatibility(dst, src) == Compatible
}

func ContainsAbstractSelf(t Type) bool {
	return containsType(t, typeTraversal{followRawPointer: true, followCallable: true}, func(candidate Type, _ bool) bool {
		named, ok := candidate.(*NamedType)
		return ok && named != nil && named.Name == "Self"
	})
}

func ContainsReference(t Type) bool {
	return containsType(t, typeTraversal{followDefined: true}, func(candidate Type, _ bool) bool {
		_, ok := candidate.(*RefType)
		return ok
	})
}

func ContainsStoredReference(t Type) bool {
	return containsType(t, typeTraversal{followDefined: true}, func(candidate Type, stored bool) bool {
		_, ok := candidate.(*RefType)
		return stored && ok
	})
}

type typeTraversal struct {
	followDefined    bool
	followRawPointer bool
	followCallable   bool
}

func containsType(t Type, traversal typeTraversal, matches func(Type, bool) bool) bool {
	type visitKey struct {
		typeValue Type
		stored    bool
	}
	seen := make(map[visitKey]struct{})
	var visit func(Type, bool) bool
	visit = func(current Type, stored bool) bool {
		if current == nil {
			return false
		}
		if matches(current, stored) {
			return true
		}
		key := visitKey{typeValue: current, stored: stored}
		if _, found := seen[key]; found {
			return false
		}
		seen[key] = struct{}{}

		switch typ := current.(type) {
		case *DefinedType:
			return traversal.followDefined && typ != nil && visit(typ.Underlying, stored)
		case *OwnedPtrType:
			return typ != nil && visit(typ.Target, true)
		case *RawPtrType:
			return traversal.followRawPointer && typ != nil && visit(typ.Target, stored)
		case *RefType:
			return typ != nil && visit(typ.Target, stored)
		case *OptionalType:
			return typ != nil && visit(typ.Inner, stored)
		case *ArrayType:
			return typ != nil && visit(typ.Elem, true)
		case *FuncType:
			if typ == nil || !traversal.followCallable {
				return false
			}
			for _, param := range typ.Params {
				if visit(param, false) {
					return true
				}
			}
			return visit(typ.Return, false)
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if visit(field.Type, true) {
					return true
				}
			}
		case *InterfaceType:
			if typ == nil || !traversal.followCallable {
				return false
			}
			for _, method := range typ.Methods {
				for _, param := range method.Params {
					if visit(param.Type, false) {
						return true
					}
				}
				if visit(method.Return, false) {
					return true
				}
			}
		}
		return false
	}
	return visit(t, false)
}

func ReplaceAbstractSelf(t Type, ownerType Type) Type {
	switch typ := t.(type) {
	case *NamedType:
		if typ != nil && typ.Name == "Self" {
			return ownerType
		}
		return t
	case *OwnedPtrType:
		if typ == nil {
			return nil
		}
		return &OwnedPtrType{Target: ReplaceAbstractSelf(typ.Target, ownerType)}
	case *RawPtrType:
		if typ == nil {
			return nil
		}
		return &RawPtrType{Target: ReplaceAbstractSelf(typ.Target, ownerType)}
	case *RefType:
		if typ == nil {
			return nil
		}
		return &RefType{Mutable: typ.Mutable, Target: ReplaceAbstractSelf(typ.Target, ownerType)}
	case *OptionalType:
		if typ == nil {
			return nil
		}
		return &OptionalType{Inner: ReplaceAbstractSelf(typ.Inner, ownerType)}
	case *ArrayType:
		if typ == nil {
			return nil
		}
		return &ArrayType{Len: typ.Len, Dynamic: typ.Dynamic, Elem: ReplaceAbstractSelf(typ.Elem, ownerType)}
	case *FuncType:
		if typ == nil {
			return nil
		}
		params := make([]Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, ReplaceAbstractSelf(param, ownerType))
		}
		consumes := append([]bool(nil), typ.Consumes...)
		return &FuncType{Params: params, Consumes: consumes, Return: ReplaceAbstractSelf(typ.Return, ownerType)}
	case *StructType:
		if typ == nil {
			return nil
		}
		fields := make([]Field, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			fields = append(fields, Field{Name: field.Name, Type: ReplaceAbstractSelf(field.Type, ownerType)})
		}
		return &StructType{Fields: fields}
	case *InterfaceType:
		if typ == nil {
			return nil
		}
		methods := make([]Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]Field, 0, len(method.Params))
			for _, param := range method.Params {
				params = append(params, Field{Name: param.Name, Type: ReplaceAbstractSelf(param.Type, ownerType)})
			}
			methods = append(methods, Method{
				Name:   method.Name,
				Params: params,
				Return: ReplaceAbstractSelf(method.Return, ownerType),
			})
		}
		return &InterfaceType{Methods: methods}
	default:
		return t
	}
}

func IsInvalid(typ Type) bool {
	_, ok := typ.(*InvalidType)
	return ok
}

func IsUnknown(typ Type) bool {
	_, ok := typ.(*UnknownType)
	return ok
}

// isInvalidOrUnknown replaces the repeated `typeinfo.IsInvalid(t) || typeinfo.IsUnknown(t)` pattern.
func IsInvalidOrUnknown(t Type) bool {
	return IsInvalid(t) || IsUnknown(t)
}
