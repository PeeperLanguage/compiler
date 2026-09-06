// Package typednil provides one canonical check for the Go interface nil
// trap: a non-nil interface value wrapping a nil pointer is not equal to nil.
// Every compiler slot that accepts an interface kind and every validator that
// dereferences one must probe through this package, so a change to the
// detection policy lands in exactly one place.
package typednil

import "reflect"

// IsNil reports whether value is nil, including the typed-nil case where a
// non-nil interface holds a nil pointer. Plain equality checks miss that case
// because the interface itself carries type information.
//
// Policy, enforced here for every caller: pointer-kind values are probed for
// nil-ness; nil maps, slices, channels, and functions report false because the
// compiler's interface slots hold pointer-backed kinds. Changing that policy
// means changing this one function.
func IsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}
