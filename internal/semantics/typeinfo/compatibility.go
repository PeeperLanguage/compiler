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

// ConversionKind identifies operation required to materialize conversion.
type ConversionKind int

const (
	ConversionNone ConversionKind = iota
	ConversionRecovery
	ConversionIdentity
	ConversionBool
	ConversionNumeric
	ConversionReference
	ConversionOptional
	ConversionStruct
)

// Conversion is canonical compatibility result shared by semantic analysis and lowering.
type Conversion struct {
	Kind          ConversionKind
	Compatibility Compatibility
}

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

// CheckCompatibility classifies conversion from src to dst without
// checker-specific context such as method-set lookup.
func CheckCompatibility(dst, src Type) Conversion {
	if dst == nil || src == nil {
		return Conversion{Kind: ConversionRecovery, Compatibility: Compatible}
	}
	if IsInvalid(dst) || IsInvalid(src) || IsUnknown(dst) || IsUnknown(src) {
		return Conversion{Kind: ConversionRecovery, Compatibility: Compatible}
	}
	if SameType(dst, src) {
		return Conversion{Kind: ConversionIdentity, Compatibility: Compatible}
	}
	if _, ok := Underlying(dst).(*BoolType); ok && IsArithmetic(src) {
		return Conversion{Kind: ConversionBool, Compatibility: ExplicitCastable}
	}
	if _, _, dstNumeric := NumericInfo(dst); dstNumeric {
		if _, _, srcNumeric := NumericInfo(src); srcNumeric {
			return Conversion{Kind: ConversionNumeric, Compatibility: checkNumericCompatibility(dst, src)}
		}
	}
	if _, dstStruct := Underlying(dst).(*StructType); dstStruct {
		if _, srcStruct := Underlying(src).(*StructType); srcStruct {
			return Conversion{Kind: ConversionStruct, Compatibility: checkStructCompatibility(dst, src)}
		}
	}
	if compat := checkRefCompatibility(dst, src); compat != Incompatible {
		return Conversion{Kind: ConversionReference, Compatibility: compat}
	}
	if compat := checkOptionalCompatibility(dst, src); compat != Incompatible {
		return Conversion{Kind: ConversionOptional, Compatibility: compat}
	}
	return Conversion{Kind: ConversionNone, Compatibility: Incompatible}
}

// checkNumericCompatibility determines if src type can be converted to dst type
// and returns the type of conversion required.
//
// Rules:
//
//   - Same type: Compatible
//   - Wider integers are compatible regardless of signedness
//   - Same-width signedness changes and integer narrowing are explicit
//   - f32 to f64 is compatible; f64 to f32 is explicit
//   - Integer, float, and byte are distinct conversion classes
//   - Cross-class numeric conversions are explicit
func checkNumericCompatibility(dst, src Type) Compatibility {
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

	if dstFamily == NumericByte || srcFamily == NumericByte {
		return ExplicitCastable
	}

	if isIntegerFamily(dstFamily) && isIntegerFamily(srcFamily) {
		if dstBits > srcBits {
			return Compatible
		}
		return ExplicitCastable
	}

	if isFloatFamily(dstFamily) && isFloatFamily(srcFamily) {
		if dstBits > srcBits {
			return Compatible
		}
		return ExplicitCastable
	}

	if (isIntegerFamily(dstFamily) && isFloatFamily(srcFamily)) ||
		(isFloatFamily(dstFamily) && isIntegerFamily(srcFamily)) {
		return ExplicitCastable
	}

	return Incompatible
}

func checkRefCompatibility(dst, src Type) Compatibility {
	left, ok := Underlying(dst).(*RefType)
	if !ok || left == nil {
		return Incompatible
	}
	right, ok := Underlying(src).(*RefType)
	if !ok || right == nil {
		return Incompatible
	}
	if !SameType(left.Target, right.Target) {
		return Incompatible
	}
	if left.Mutable && !right.Mutable {
		return Incompatible
	}
	return Compatible
}

func checkOptionalCompatibility(dst, src Type) Compatibility {
	left, ok := Underlying(dst).(*OptionalType)
	if !ok || left == nil {
		return Incompatible
	}
	if _, ok := Underlying(src).(*NoneType); ok {
		return Compatible
	}
	if SameType(left.Inner, src) {
		return Compatible
	}
	right, ok := Underlying(src).(*OptionalType)
	if ok && right != nil {
		if SameType(left.Inner, right.Inner) {
			return Compatible
		}
		return Incompatible
	}
	return Incompatible
}

func checkStructCompatibility(dst, src Type) Compatibility {
	dstStruct, dstNominal := nominalStructType(dst)
	srcStruct, srcNominal := nominalStructType(src)
	left, ok := Underlying(dst).(*StructType)
	if !ok || left == nil {
		return Incompatible
	}
	right, ok := Underlying(src).(*StructType)
	if !ok || right == nil || len(left.Fields) != len(right.Fields) {
		return Incompatible
	}
	if !left.isSameType(right) {
		return Incompatible
	}
	if dstNominal {
		if srcNominal && dstStruct.Identity == srcStruct.Identity {
			return Compatible
		}
		return ExplicitCastable
	}
	return Compatible
}

// isIntegerFamily returns true for signed and unsigned integer families
func isIntegerFamily(f NumericFamily) bool {
	return f == NumericSigned || f == NumericUnsigned
}

// isFloatFamily returns true for floating-point family
func isFloatFamily(f NumericFamily) bool {
	return f == NumericFloat
}
