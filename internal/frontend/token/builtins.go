package token

import (
	"strconv"
	"strings"

	"compiler/internal/target"
)

func IsBuiltinType(name string) bool {
	switch name {
	case "bool", "char", "str", "usize", "isize", "f32", "f64", "void":
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
	case "byte":
		return false, 8, true
	}
	if len(name) < 2 {
		return false, 0, false
	}
	switch name[0] {
	case 'i':
		signed = true
	case 'u':
		signed = false
	default:
		return false, 0, false
	}
	if strings.HasPrefix(name, "i0") || strings.HasPrefix(name, "u0") {
		return false, 0, false
	}
	n, err := strconv.Atoi(name[1:])
	if err != nil || n < 8 {
		return false, 0, false
	}
	if n&(n-1) != 0 {
		return false, 0, false
	}
	return signed, n, true
}
