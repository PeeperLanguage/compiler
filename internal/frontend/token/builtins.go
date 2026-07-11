package token

import (
	"strconv"
	"strings"

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
	if err != nil || n < 1 || n > numeric.MaxIntegerBits {
		return false, 0, false
	}
	return signed, n, true
}
