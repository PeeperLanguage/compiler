package typeinfo

import "compiler/pkg/numeric"

// Capability queries for typing rules.
// Keep checker dumb: checker asks "can op apply?", type system answers.

func DefaultNumberType(value string) Type {
	if numeric.IsFloat(value) {
		return &FloatType{Bits: 64}
	}
	for bits := 32; ; bits *= 2 {
		if numeric.FitsIntegerLiteral(value, bits, true) {
			return &IntegerType{Signed: true, Bits: bits}
		}
		if bits == numeric.MaxIntegerBits {
			break
		}
	}
	return &InvalidType{}
}

func DefaultIntegerType() Type {
	return &IntegerType{Signed: true, Bits: 32}
}

func LiteralFitsType(value string, typ Type) bool {
	switch t := Underlying(typ).(type) {
	case *IntegerType:
		return numeric.FitsIntegerLiteral(value, t.Bits, t.Signed)
	case *FloatType:
		if numeric.IsFloat(value) {
			return numeric.FitsFloatLiteral(value, t.Bits)
		}
		return numeric.FitsIntegerLiteralInFloat(value, t.Bits)
	case *ByteType:
		return numeric.FitsIntegerLiteral(value, 8, false)
	default:
		return false
	}
}

func IsIntegral(t Type) bool {
	t = Underlying(t)
	switch t.(type) {
	case *IntegerType, *ByteType:
		return true
	default:
		return false
	}
}

func IsArithmetic(t Type) bool {
	t = Underlying(t)
	switch t.(type) {
	case *IntegerType, *ByteType, *FloatType:
		return true
	default:
		return false
	}
}

func IsEquatable(t Type) bool {
	t = Underlying(t)
	switch t.(type) {
	case *IntegerType, *ByteType, *CharType, *FloatType, *BoolType, *CStrType, *StringType, *NoneType, *AllocatorType:
		return true
	case *OptionalType:
		return true
	default:
		return false
	}
}

func IsCondition(t Type) bool {
	t = Underlying(t)
	_, ok := t.(*BoolType)
	return ok
}

func IsImplicitCopyType(t Type) bool {
	switch typ := Underlying(t).(type) {
	case *IntegerType, *ByteType, *CharType, *FloatType, *BoolType, *CStrType, *RawPtrType, *AllocatorType:
		return true
	case *RefType:
		return typ != nil && !typ.Mutable
	case *OptionalType:
		return typ != nil && IsImplicitCopyType(typ.Inner)
	default:
		return false
	}
}

func IsSizedType(t Type) bool {
	visiting := make(map[*DefinedType]bool)
	var check func(Type) bool
	check = func(current Type) bool {
		if current == nil {
			return false
		}
		if defined, ok := current.(*DefinedType); ok {
			if defined == nil || visiting[defined] {
				return false
			}
			visiting[defined] = true
			defer delete(visiting, defined)
			return check(defined.Underlying)
		}
		switch typ := Underlying(current).(type) {
		case *InvalidType, *UnknownType, *InterfaceType:
			return false
		case *IntegerType, *ByteType, *CharType, *FloatType, *BoolType, *CStrType, *StringType, *NoneType, *NamedType, *EnumType, *AllocatorType:
			return true
		case *OwnedPtrType:
			return typ != nil && typ.Target != nil
		case *RefType:
			return typ != nil && typ.Target != nil
		case *RawPtrType:
			return typ != nil
		case *OptionalType:
			return typ != nil && check(typ.Inner)
		case *ArrayType:
			if typ == nil || typ.Elem == nil {
				return false
			}
			if typ.Shape == ArraySlice {
				return false
			}
			return check(typ.Elem)
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if !check(field.Type) {
					return false
				}
			}
			return true
		case *FuncType:
			if typ == nil {
				return false
			}
			for _, param := range typ.Params {
				if !check(param) {
					return false
				}
			}
			return typ.Return == nil || check(typ.Return)
		default:
			return false
		}
	}
	return check(t)
}

func IsNoCopyType(t Type) bool {
	seen := make(map[*DefinedType]bool)
	var check func(Type) bool
	check = func(current Type) bool {
		switch typ := current.(type) {
		case *DefinedType:
			if typ == nil || seen[typ] {
				return false
			}
			seen[typ] = true
			return check(typ.Underlying)
		}
		switch typ := Underlying(current).(type) {
		case *OwnedPtrType, *StringType, *InterfaceType:
			return true
		case *RefType:
			return typ != nil && typ.Mutable
		case *OptionalType:
			return typ != nil && check(typ.Inner)
		case *ArrayType:
			return typ != nil && (typ.Shape == ArrayOwner || check(typ.Elem))
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if check(field.Type) {
					return true
				}
			}
		}
		return false
	}
	return check(t)
}

// IsLowerableType reports whether current backend lowering can represent type.
// Owned pointers close recursive named composites without expanding storage;
// abstract Self parameters remain semantic-only interface metadata.
func IsLowerableType(t Type) bool {
	visiting := make(map[Type]struct{})
	var check func(Type) bool
	check = func(t Type) bool {
		t = Underlying(t)
		if t == nil {
			return false
		}
		if _, found := visiting[t]; found {
			return false
		}
		visiting[t] = struct{}{}
		defer delete(visiting, t)

		switch typ := t.(type) {
		case *IntegerType, *ByteType, *CharType, *FloatType, *BoolType, *CStrType, *StringType, *AllocatorType:
			return true
		case *OwnedPtrType:
			target, ok := PointerTarget(typ)
			return ok && target != nil
		case *RawPtrType:
			return typ != nil
		case *RefType:
			if typ == nil || typ.Target == nil {
				return false
			}
			if _, nested := Underlying(typ.Target).(*RefType); nested {
				return false
			}
			if target, ok := Underlying(typ.Target).(*ArrayType); ok && target != nil && target.Shape == ArraySlice {
				return target.Elem != nil && check(target.Elem)
			}
			return check(typ.Target)
		case *OptionalType:
			return typ != nil && typ.Inner != nil && check(typ.Inner)
		case *ArrayType:
			return typ != nil && typ.Shape != ArraySlice && (typ.Shape == ArrayOwner || typ.Len != "") && typ.Elem != nil && check(typ.Elem)
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if !check(field.Type) {
					return false
				}
			}
			return true
		case *InterfaceType:
			if typ == nil {
				return false
			}
			for _, method := range typ.Methods {
				if len(method.Params) == 0 {
					return false
				}
				for i, param := range method.Params {
					if i == 0 {
						continue
					}
					if ContainsAbstractSelf(param.Type) || !check(param.Type) {
						return false
					}
				}
				if method.Return != nil && (ContainsAbstractSelf(method.Return) || !check(method.Return)) {
					return false
				}
			}
			return true
		case *FuncType:
			if typ == nil {
				return false
			}
			for _, param := range typ.Params {
				if !check(param) {
					return false
				}
			}
			return typ.Return == nil || check(typ.Return)
		case *EnumType:
			return typ != nil
		default:
			return false
		}
	}
	return check(t)
}

// NeedsDrop reports whether normal scope cleanup must destroy runtime-owned
// state reachable through a value. Move-only borrows and plain composites do
// not need destruction; this is intentionally narrower than IsNoCopyType.
func NeedsDrop(t Type) bool {
	seen := make(map[*DefinedType]bool)
	var check func(Type) bool
	check = func(current Type) bool {
		switch typ := current.(type) {
		case *DefinedType:
			if typ == nil || seen[typ] {
				return false
			}
			seen[typ] = true
			defer delete(seen, typ)
			return check(typ.Underlying)
		}
		switch typ := Underlying(current).(type) {
		case *OwnedPtrType, *StringType:
			return true
		case *OptionalType:
			return typ != nil && check(typ.Inner)
		case *ArrayType:
			return typ != nil && (typ.Shape == ArrayOwner || check(typ.Elem))
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if check(field.Type) {
					return true
				}
			}
		}
		return false
	}
	return check(t)
}
