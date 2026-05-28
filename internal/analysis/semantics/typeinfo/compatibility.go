package typeinfo

// Compatibility indicates the type of conversion allowed between types
type Compatibility int

const (
	// Compatible means implicit conversion is allowed (safe, no data loss)
	Compatible Compatibility = iota
	// ExplicitCastable means conversion requires an explicit cast
	ExplicitCastable
	// Incompatible means the types cannot be converted
	Incompatible
)

// String returns the string representation of the Compatibility value
func (c Compatibility) String() string {
	switch c {
	case Compatible:
		return "compatible"
	case ExplicitCastable:
		return "explicit_castable"
	case Incompatible:
		return "incompatible"
	default:
		return "unknown"
	}
}

// CheckNumericCompatibility determines if src type can be converted to dst type
// and returns the type of conversion required.
//
// Rules (inspired by Rust/Go strictness + Java widening):
//
//	- Same type: Compatible
//	- Byte (u8) to/from any: ExplicitCastable
//	- Same family, dst larger: Compatible (widening)
//	- Same family, dst smaller: ExplicitCastable (narrowing)
//	- Integer to Float: Compatible only if float can represent all integer values exactly
//	- Float to Integer: ExplicitCastable (fractional part loss)
//	- Signed <-> Unsigned: ExplicitCastable
//	- Different families: Incompatible
func CheckNumericCompatibility(dst, src Type) Compatibility {
	// Same type: always compatible
	if SameType(dst, src) {
		return Compatible
	}

	dstFamily, dstBits, okDst := NumericInfo(dst)
	srcFamily, srcBits, okSrc := NumericInfo(src)

	// If either is not numeric, they're incompatible
	if !okDst || !okSrc {
		return Incompatible
	}

	// === BYTE RULE ===
	// Byte (u8) always requires explicit cast to/from any type
	if isByte(dst) || isByte(src) {
		return ExplicitCastable
	}

	// === SAME FAMILY ===
	if dstFamily == srcFamily {
		if dstBits >= srcBits {
			return Compatible // Widening: i8→i16, i16→i32, f32→f64
		}
		return ExplicitCastable // Narrowing: i16→i8, f64→f32
	}

	// === INTEGER → FLOAT ===
	if isIntegerFamily(srcFamily) && isFloatFamily(dstFamily) {
		// f64 can represent all i32 and smaller exactly (53-bit mantissa)
		if dstBits == 64 && srcBits <= 32 {
			return Compatible
		}
		// f32 can represent all i16 and smaller exactly (24-bit mantissa)
		if dstBits == 32 && srcBits <= 16 {
			return Compatible
		}
		return ExplicitCastable // i32→f32: loss of precision for values > 2^24
	}

	// === FLOAT → INTEGER ===
	// Always explicit cast required (fractional part loss)
	if isFloatFamily(srcFamily) && isIntegerFamily(dstFamily) {
		return ExplicitCastable
	}

	// === SIGNED ↔ UNSIGNED ===
	if isSignedFamily(srcFamily) != isSignedFamily(dstFamily) {
		return ExplicitCastable
	}

	return Incompatible
}

// isByte checks if the type represents a byte (u8)
func isByte(t Type) bool {
	if t == nil {
		return false
	}
	switch typ := t.(type) {
	case *IntegerType:
		return !typ.Signed && typ.Bits == 8
	case *NamedType:
		return typ.Name == "byte" || typ.Name == "u8"
	default:
		return false
	}
}

// isIntegerFamily returns true for signed and unsigned integer families
func isIntegerFamily(f NumericFamily) bool {
	return f == NumericSigned || f == NumericUnsigned
}

// isFloatFamily returns true for floating-point family
func isFloatFamily(f NumericFamily) bool {
	return f == NumericFloat
}

// isSignedFamily returns true for signed integer family
func isSignedFamily(f NumericFamily) bool {
	return f == NumericSigned
}

// isUnsignedFamily returns true for unsigned integer family
func isUnsignedFamily(f NumericFamily) bool {
	return f == NumericUnsigned
}
