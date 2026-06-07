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

// CheckCompatibility determines whether src can be used as dst without
// checker-specific context such as method-set lookup.
func CheckCompatibility(dst, src Type) Compatibility {
	if dst == nil || src == nil {
		return Compatible
	}
	if IsInvalid(dst) || IsInvalid(src) || IsUnknown(dst) || IsUnknown(src) {
		return Compatible
	}
	if SameType(dst, src) {
		return Compatible
	}
	if compat := CheckNumericCompatibility(dst, src); compat != Incompatible {
		return compat
	}
	if compat := checkPointerCompatibility(dst, src); compat != Incompatible {
		return compat
	}
	if compat := checkFuncCompatibility(dst, src); compat != Incompatible {
		return compat
	}
	if compat := checkStructCompatibility(dst, src); compat != Incompatible {
		return compat
	}
	if compat := checkInterfaceCompatibility(dst, src); compat != Incompatible {
		return compat
	}
	return checkEnumCompatibility(dst, src)
}

// CheckNumericCompatibility determines if src type can be converted to dst type
// and returns the type of conversion required.
//
// Rules (inspired by Rust/Go strictness + Java widening):
//
//   - Same type: Compatible
//   - Byte (u8) to/from any: ExplicitCastable
//   - Same family, dst larger: Compatible (widening)
//   - Same family, dst smaller: ExplicitCastable (narrowing)
//   - Integer to Float: Compatible only if float can represent all integer values exactly
//   - Float to Integer: ExplicitCastable (fractional part loss)
//   - Signed <-> Unsigned: ExplicitCastable
//   - Different families: Incompatible
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

func checkPointerCompatibility(dst, src Type) Compatibility {
	left, ok := Underlying(dst).(*RawPtrType)
	if !ok || left == nil {
		return Incompatible
	}
	right, ok := Underlying(src).(*RawPtrType)
	if !ok || right == nil || left.Mutable != right.Mutable {
		return Incompatible
	}
	if SameType(left.Target, right.Target) {
		return Compatible
	}
	return Incompatible
}

func checkFuncCompatibility(dst, src Type) Compatibility {
	left, ok := Underlying(dst).(*FuncType)
	if !ok || left == nil {
		return Incompatible
	}
	right, ok := Underlying(src).(*FuncType)
	if !ok || right == nil || len(left.Params) != len(right.Params) {
		return Incompatible
	}
	for i := range left.Params {
		if !SameType(left.Params[i], right.Params[i]) {
			return Incompatible
		}
	}
	if !SameType(left.Return, right.Return) {
		return Incompatible
	}
	return Compatible
}

func checkStructCompatibility(dst, src Type) Compatibility {
	left, ok := Underlying(dst).(*StructType)
	if !ok || left == nil {
		return Incompatible
	}
	right, ok := Underlying(src).(*StructType)
	if !ok || right == nil || len(left.Fields) != len(right.Fields) {
		return Incompatible
	}
	for i := range left.Fields {
		if left.Fields[i].Name != right.Fields[i].Name || !SameType(left.Fields[i].Type, right.Fields[i].Type) {
			return Incompatible
		}
	}
	return Compatible
}

func checkInterfaceCompatibility(dst, src Type) Compatibility {
	left, ok := Underlying(dst).(*InterfaceType)
	if !ok || left == nil {
		return Incompatible
	}
	right, ok := Underlying(src).(*InterfaceType)
	if !ok || right == nil || len(left.Methods) != len(right.Methods) {
		return Incompatible
	}
	for i := range left.Methods {
		leftMethod := left.Methods[i]
		rightMethod := right.Methods[i]
		if leftMethod.Name != rightMethod.Name || len(leftMethod.Params) != len(rightMethod.Params) {
			return Incompatible
		}
		for j := range leftMethod.Params {
			if !SameType(leftMethod.Params[j].Type, rightMethod.Params[j].Type) {
				return Incompatible
			}
		}
		if !SameType(leftMethod.Return, rightMethod.Return) {
			return Incompatible
		}
	}
	return Compatible
}

func checkEnumCompatibility(dst, src Type) Compatibility {
	left, ok := Underlying(dst).(*EnumType)
	if !ok || left == nil {
		return Incompatible
	}
	right, ok := Underlying(src).(*EnumType)
	if !ok || right == nil || len(left.Variants) != len(right.Variants) {
		return Incompatible
	}
	for i := range left.Variants {
		if left.Variants[i] != right.Variants[i] {
			return Incompatible
		}
	}
	return Compatible
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
