package typeinfo

import "slices"

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
	if compat := checkRefCompatibility(dst, src); compat != Incompatible {
		return compat
	}
	if compat := checkOptionalCompatibility(dst, src); compat != Incompatible {
		return compat
	}
	if compat := checkFuncCompatibility(dst, src); compat != Incompatible {
		return compat
	}
	if compat := checkArrayCompatibility(dst, src); compat != Incompatible {
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
// Rules:
//
//   - Same type: Compatible
//   - Wider integers are compatible regardless of signedness
//   - Same-width signedness changes and integer narrowing are explicit
//   - f32 to f64 is compatible; f64 to f32 is explicit
//   - Integer, float, and byte are distinct conversion classes
//   - Cross-class numeric conversions are explicit
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

func checkPointerCompatibility(dst, src Type) Compatibility {
	left, ok := Underlying(dst).(*RawPtrType)
	if !ok || left == nil {
		return Incompatible
	}
	right, ok := Underlying(src).(*RawPtrType)
	if !ok || right == nil {
		return Incompatible
	}
	return Compatible
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
	right, ok := Underlying(src).(*OptionalType)
	if ok && right != nil {
		if SameType(left.Inner, right.Inner) {
			return Compatible
		}
		return Incompatible
	}
	if SameType(left.Inner, src) {
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
	if !sameReturnOriginContract(left.ReturnOrigins, right.ReturnOrigins) {
		return Incompatible
	}
	return Compatible
}

func checkArrayCompatibility(dst, src Type) Compatibility {
	switch left := Underlying(dst).(type) {
	case *ArrayType:
		right, ok := Underlying(src).(*ArrayType)
		if !ok || right == nil {
			return Incompatible
		}
		if left.Len == right.Len && left.Shape == right.Shape && SameType(left.Elem, right.Elem) {
			return Compatible
		}
	}
	return Incompatible
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
		if !sameReturnOriginContract(leftMethod.ReturnOrigins, rightMethod.ReturnOrigins) {
			return Incompatible
		}
	}
	return Compatible
}

func sameReturnOriginContract(left, right *ReturnOriginContract) bool {
	if left == nil || right == nil {
		return left == right
	}
	if len(left.Sources) != len(right.Sources) {
		return false
	}
	for _, source := range left.Sources {
		if !slices.Contains(right.Sources, source) {
			return false
		}
	}
	return true
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

// isIntegerFamily returns true for signed and unsigned integer families
func isIntegerFamily(f NumericFamily) bool {
	return f == NumericSigned || f == NumericUnsigned
}

// isFloatFamily returns true for floating-point family
func isFloatFamily(f NumericFamily) bool {
	return f == NumericFloat
}
