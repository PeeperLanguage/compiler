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
	if !sameStructFields(left, right) {
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

func sameStructFields(left, right *StructType) bool {
	if left == nil || right == nil || len(left.Fields) != len(right.Fields) {
		return false
	}
	rightFields := make(map[string]Type, len(right.Fields))
	for _, field := range right.Fields {
		if field.Name == "" {
			return false
		}
		if _, exists := rightFields[field.Name]; exists {
			return false
		}
		rightFields[field.Name] = field.Type
	}
	for _, field := range left.Fields {
		rightType, ok := rightFields[field.Name]
		if !ok || !SameType(field.Type, rightType) {
			return false
		}
		delete(rightFields, field.Name)
	}
	return len(rightFields) == 0
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
	if same, nominal := sameNominalEnum(dst, src); nominal {
		if same {
			return Compatible
		}
		return Incompatible
	}
	left, ok := Underlying(dst).(*EnumType)
	if !ok || left == nil {
		return Incompatible
	}
	right, ok := Underlying(src).(*EnumType)
	if !ok || right == nil || len(left.Cases) != len(right.Cases) {
		return Incompatible
	}
	for i := range left.Cases {
		if left.Cases[i].Name != right.Cases[i].Name || !SameType(left.Cases[i].Payload, right.Cases[i].Payload) {
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
