package typeinfo

// IsOptional reports whether typ has optional representation after aliases are resolved.
func IsOptional(typ Type) bool {
	_, ok := Underlying(typ).(*OptionalType)
	return ok
}

// OptionalLayerCount returns the number of nested optional layers around typ.
func OptionalLayerCount(typ Type) int {
	depth := 0
	for {
		optional, ok := Underlying(typ).(*OptionalType)
		if !ok || optional == nil || optional.Inner == nil {
			return depth
		}
		depth++
		typ = optional.Inner
	}
}

// UnwrapOptionalLayers removes at most depth optional layers from typ.
func UnwrapOptionalLayers(typ Type, depth int) Type {
	for range depth {
		optional, ok := Underlying(typ).(*OptionalType)
		if !ok || optional == nil || optional.Inner == nil {
			break
		}
		typ = optional.Inner
	}
	return typ
}

// OptionalPayloadDepthForExpected returns the number of optional payload layers
// required before src is assignable to a non-optional expected type.
func OptionalPayloadDepthForExpected(src, expected Type) int {
	if src == nil || expected == nil || IsOptional(expected) {
		return 0
	}
	current := src
	for depth := 1; ; depth++ {
		optional, ok := Underlying(current).(*OptionalType)
		if !ok || optional == nil || optional.Inner == nil {
			return 0
		}
		current = optional.Inner
		if Assignable(expected, current) {
			return depth
		}
	}
}
