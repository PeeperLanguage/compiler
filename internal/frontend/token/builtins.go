package token

import (
	"compiler/internal/target"
	"compiler/pkg/numeric"
)

func IsBuiltinType(name string) bool {
	switch name {
	case "bool", "byte", "char", "str", "usize", "isize", "f32", "f64":
		return true
	default:
		_, _, ok := ParseIntegerBuiltin(name)
		return ok
	}
}

func ParseIntegerBuiltin(name string) (signed bool, bits int, ok bool) {
	switch name {
	case "isize":
		return true, target.SizeBits(), true
	case "usize":
		return false, target.SizeBits(), true
	}
	return numeric.ParseIntegerTypeName(name)
}
