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
	case *IntegerType, *FloatType, *BoolType:
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
	t = Underlying(t)
	switch typ := t.(type) {
	case nil:
		return false
	case *InvalidType, *UnknownType:
		return false
	case *IntegerType, *FloatType, *BoolType, *CStrType:
		return true
	case *RawPtrType:
		return true
	case *FuncType:
		if typ == nil {
			return false
		}
		return true
	case *EnumType:
		return typ != nil
	case *StructType:
		// Conservative v1 rule: structs are move-only until Ember grows an
		// explicit Copy story for user-defined aggregate types.
		return false
	default:
		return false
	}
}
