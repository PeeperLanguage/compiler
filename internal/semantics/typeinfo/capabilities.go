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
	}
}

func DefaultIntegerType() Type {
	return &IntegerType{Signed: true, Bits: 32}
}

func IsIntegral(t Type) bool {
	t = Underlying(t)
	_, ok := t.(*IntegerType)
	return ok
}

func IsArithmetic(t Type) bool {
	t = Underlying(t)
	switch t.(type) {
	case *IntegerType, *FloatType:
		return true
	default:
		return false
	}
}

func IsEquatable(t Type) bool {
	t = Underlying(t)
	switch t.(type) {
	case *IntegerType, *FloatType, *BoolType, *CStrType, *StringType, *NoneType:
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

func IsCopyType(t Type) bool {
	switch typ := t.(type) {
	case *DefinedType:
		if typ == nil {
			return false
		}
		switch typ.CopyMode {
		case CopyAllow:
			return true
		case CopyDeny:
			return false
		default:
			return IsCopyType(typ.Underlying)
		}
	}
	t = Underlying(t)
	switch typ := t.(type) {
	case nil:
		return false
	case *InvalidType, *UnknownType:
		return false
	case *IntegerType, *FloatType, *BoolType, *CStrType, *StringType, *NoneType:
		return true
	case *RawPtrType:
		return typ != nil && !typ.Mutable
	case *OptionalType:
		return typ != nil && IsCopyType(typ.Inner)
	case *ArrayType:
		return typ != nil && IsCopyType(typ.Elem)
	case *SliceType:
		return false
	case *FuncType:
		if typ == nil {
			return false
		}
		return true
	case *InterfaceType:
		return typ != nil
	case *EnumType:
		return typ != nil
	case *StructType:
		if typ == nil {
			return false
		}
		for _, field := range typ.Fields {
			if !IsCopyType(field.Type) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
