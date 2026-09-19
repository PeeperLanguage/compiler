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
	family, bits, ok := NumericInfo(typ)
	if !ok {
		return false
	}
	switch family {
	case NumericSigned, NumericUnsigned, NumericByte:
		return numeric.FitsIntegerLiteral(value, bits, family == NumericSigned)
	case NumericFloat:
		if numeric.IsFloat(value) {
			return numeric.FitsFloatLiteral(value, bits)
		}
		return numeric.FitsIntegerLiteralInFloat(value, bits)
	default:
		return false
	}
}

func IsIntegral(t Type) bool {
	family, _, ok := NumericInfo(t)
	return ok && family != NumericFloat
}

func IsArithmetic(t Type) bool {
	_, _, ok := NumericInfo(t)
	return ok
}

func IsOrderable(t Type) bool {
	if _, _, ok := NumericInfo(t); ok {
		return true
	}
	_, ok := Underlying(t).(*CharType)
	return ok
}

func IsEquatable(t Type) bool {
	if _, _, ok := NumericInfo(t); ok {
		return true
	}
	switch Underlying(t).(type) {
	case *CharType, *BoolType, *CStrType, *RawPtrType, *StringType, *NoneType, *AllocatorType, *OptionalType:
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
