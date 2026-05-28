package typeinfo

import "compiler/internal/utils/numeric"

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
	_, ok := t.(*IntegerType)
	return ok
}

func IsArithmetic(t Type) bool {
	switch t.(type) {
	case *IntegerType, *FloatType:
		return true
	default:
		return false
	}
}

func IsOrderable(t Type) bool {
	return IsArithmetic(t)
}

func IsEquatable(t Type) bool {
	switch t.(type) {
	case *IntegerType, *FloatType, *BoolType:
		return true
	default:
		return false
	}
}

func IsLogical(t Type) bool {
	return IsCondition(t)
}

func IsCondition(t Type) bool {
	if IsArithmetic(t) {
		return true
	}
	_, ok := t.(*BoolType)
	return ok
}
