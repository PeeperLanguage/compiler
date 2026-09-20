package typeinfo

import (
	"compiler/internal/frontend/token"
	"compiler/internal/target"
	"compiler/pkg/numeric"
)

// SameType preserves nominal declaration identity and transparent aliases before
// delegating intrinsic equality to the normalized semantic type.
func SameType(left, right Type) bool {
	if left == right {
		return true
	}
	if same, nominal := sameNominalEnum(left, right); nominal {
		return same
	}
	if same, nominal := sameNominalStruct(left, right); nominal {
		return same
	}
	left = Underlying(left)
	right = Underlying(right)
	if left == nil {
		return right == nil
	}
	return left.isSameType(right)
}

func sameNominalEnum(left, right Type) (same, nominal bool) {
	leftIdentity, leftNominal := nominalEnumIdentity(left)
	rightIdentity, rightNominal := nominalEnumIdentity(right)
	if !leftNominal && !rightNominal {
		return false, false
	}
	return leftNominal && rightNominal && leftIdentity != "" && leftIdentity == rightIdentity, true
}

func nominalEnumIdentity(typ Type) (string, bool) {
	defined, ok := Unalias(typ).(*DefinedType)
	if !ok || defined == nil || defined.Kind != DefinedKindEnum {
		return "", false
	}
	return defined.Identity, true
}

func sameNominalStruct(left, right Type) (same, nominal bool) {
	leftType, leftNominal := nominalStructType(left)
	rightType, rightNominal := nominalStructType(right)
	if !leftNominal && !rightNominal {
		return false, false
	}
	return leftNominal && rightNominal && leftType.Identity != "" && leftType.Identity == rightType.Identity, true
}

func nominalStructType(typ Type) (*DefinedType, bool) {
	defined, ok := Unalias(typ).(*DefinedType)
	return defined, ok && defined != nil && defined.Kind == DefinedKindStruct
}

type NumericFamily int

const (
	NumericInvalid NumericFamily = iota
	NumericSigned
	NumericUnsigned
	NumericByte
	NumericFloat
)

type numericType interface {
	numericInfo() (family NumericFamily, bits int, ok bool)
}

func NumericInfo(t Type) (family NumericFamily, bits int, ok bool) {
	numeric, ok := Underlying(t).(numericType)
	if !ok {
		return NumericInvalid, 0, false
	}
	return numeric.numericInfo()
}

func (t *IntegerType) numericInfo() (NumericFamily, int, bool) {
	if t == nil {
		return NumericInvalid, 0, false
	}
	if t.Signed {
		return NumericSigned, t.Bits, true
	}
	return NumericUnsigned, t.Bits, true
}

func (*ByteType) numericInfo() (NumericFamily, int, bool) {
	return NumericByte, 8, true
}

func (t *FloatType) numericInfo() (NumericFamily, int, bool) {
	if t == nil {
		return NumericInvalid, 0, false
	}
	return NumericFloat, t.Bits, true
}

func (t *NamedType) numericInfo() (NumericFamily, int, bool) {
	if t == nil {
		return NumericInvalid, 0, false
	}
	if t.Name == "byte" {
		return NumericByte, 8, true
	}
	if signed, bits, ok := numeric.ParseIntegerTypeName(t.Name); ok {
		if signed {
			return NumericSigned, bits, true
		}
		return NumericUnsigned, bits, true
	}
	switch t.Name {
	case "f32":
		return NumericFloat, 32, true
	case "f64":
		return NumericFloat, 64, true
	default:
		return NumericInvalid, 0, false
	}
}

// NumericTypeFromName is the canonical bridge from explicit source type text
// to semantic numeric identity. Arbitrary float widths stay rejected until the
// language has a representation independent from LLVM's target float set.
func NumericTypeFromName(name string, targetInfo target.Info) (Type, bool) {
	if !targetInfo.Valid() {
		targetInfo = target.Host()
	}
	if signed, bits, ok := token.ParseIntegerBuiltin(name, targetInfo); ok {
		return &IntegerType{Signed: signed, Bits: bits}, true
	}
	switch name {
	case "f32":
		return &FloatType{Bits: 32}, true
	case "f64":
		return &FloatType{Bits: 64}, true
	default:
		return nil, false
	}
}

func CommonNumericType(a, b Type) Type {
	if _, _, ok := NumericInfo(a); !ok {
		return nil
	}
	if _, _, ok := NumericInfo(b); !ok {
		return nil
	}
	if SameType(a, b) {
		return a
	}
	if checkNumericCompatibility(a, b) == Compatible {
		return a
	}
	if checkNumericCompatibility(b, a) == Compatible {
		return b
	}
	return nil
}

func Assignable(dst, src Type) bool {
	return CheckCompatibility(dst, src).Compatibility == Compatible
}

func ContainsTypeParameter(t Type) bool {
	return containsType(t, typeTraversal{followDefined: true, followCallable: true}, func(candidate Type, _ bool) bool {
		_, ok := candidate.(*TypeParameterType)
		return ok
	})
}

func ContainsInvalid(t Type) bool {
	return containsType(t, typeTraversal{followDefined: true, followCallable: true}, func(candidate Type, _ bool) bool {
		return IsInvalid(candidate)
	})
}

func ContainsReference(t Type) bool {
	return containsType(t, typeTraversal{followDefined: true}, func(candidate Type, _ bool) bool {
		_, ok := candidate.(*RefType)
		return ok
	})
}

func ContainsStoredReference(t Type) bool {
	return containsType(t, typeTraversal{followDefined: true, referenceLeaf: true}, func(candidate Type, stored bool) bool {
		_, ok := candidate.(*RefType)
		return stored && ok
	})
}

func ContainsNamedEnum(t Type) bool {
	return containsType(t, typeTraversal{followDefined: true, followCallable: true}, func(candidate Type, _ bool) bool {
		descriptor, ok := VariantDescriptorOf(candidate)
		return ok && descriptor.Family == VariantFamilyNamed
	})
}

type typeTraversal struct {
	followDefined  bool
	followCallable bool
	referenceLeaf  bool
}

func containsType(t Type, traversal typeTraversal, matches func(Type, bool) bool) bool {
	type visitKey struct {
		typeValue Type
		stored    bool
	}
	seen := make(map[visitKey]struct{})
	var visit func(Type, bool) bool
	visit = func(current Type, stored bool) bool {
		if current == nil {
			return false
		}
		if matches(current, stored) {
			return true
		}
		key := visitKey{typeValue: current, stored: stored}
		if _, found := seen[key]; found {
			return false
		}
		seen[key] = struct{}{}

		matched := false
		ForEachChild(current, func(child TypeChild) bool {
			childStored, follow := traversedChildState(child.Relation, stored, traversal)
			if !follow {
				return true
			}
			if visit(child.Type, childStored) {
				matched = true
				return false
			}
			return true
		})
		return matched
	}
	return visit(t, false)
}

func traversedChildState(relation TypeChildRelation, stored bool, traversal typeTraversal) (bool, bool) {
	switch relation {
	case TypeChildUnderlying:
		return stored, traversal.followDefined
	case TypeChildOwnedTarget, TypeChildArrayElement, TypeChildStructField, TypeChildEnumPayload:
		return true, true
	case TypeChildBorrowedTarget:
		return stored, !traversal.referenceLeaf
	case TypeChildOptionalPayload:
		return stored, true
	case TypeChildCallableParameter, TypeChildCallableReturn:
		return false, traversal.followCallable
	case TypeChildTypeParameter, TypeChildTypeArgument:
		return false, false
	default:
		panic("typeinfo: unknown semantic type child relation")
	}
}

func IsInvalid(typ Type) bool {
	_, ok := typ.(*InvalidType)
	return ok
}

func IsUnknown(typ Type) bool {
	_, ok := typ.(*UnknownType)
	return ok
}

// isInvalidOrUnknown replaces the repeated `typeinfo.IsInvalid(t) || typeinfo.IsUnknown(t)` pattern.
func IsInvalidOrUnknown(t Type) bool {
	return IsInvalid(t) || IsUnknown(t)
}
