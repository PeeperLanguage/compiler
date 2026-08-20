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

func ParseIntegerBuiltin(name string, targetInfo target.Info) (signed bool, bits int, ok bool) {
	switch name {
	case "isize":
		return true, targetInfo.PointerBits, targetInfo.Valid()
	case "usize":
		return false, targetInfo.PointerBits, targetInfo.Valid()
	}
	return numeric.ParseIntegerTypeName(name)
}
