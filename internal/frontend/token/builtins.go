package token

import (
	"compiler/internal/target"
	"compiler/pkg/numeric"
)

func IsBuiltinType(name string) bool {
	switch name {
	case "bool", "byte", "char", "str", "usize", "isize", "f32", "f64": // fN not supported yet. So f32 and f64 is put here.
		return true
	default:
		_, _, ok := numeric.ParseIntegerTypeName(name)
		return ok
	}
}

func ParseIntegerBuiltin(name string, targetInfo target.Info) (isSigned bool, bits int, ok bool) {
	switch name {
	case "isize":
		return true, targetInfo.PointerBits, targetInfo.IsValid()
	case "usize":
		return false, targetInfo.PointerBits, targetInfo.IsValid()
	}
	return numeric.ParseIntegerTypeName(name)
}
