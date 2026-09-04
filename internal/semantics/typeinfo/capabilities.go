package typeinfo

import "compiler/pkg/numeric"

// Capability queries for typing rules.
// Keep checker dumb: checker asks "can op apply?", type system answers.

func DefaultNumberType(value string) Type {
	if numeric.IsFloat(value) {
		return &FloatType{Bits: 64}
	}
	for bits := 32; ; bits *= 2 {
		if numeric.FitsIntegerLiteral(value, bits, true) {
			return &IntegerType{Signed: true, Bits: bits}
		}
		if bits == numeric.MaxIntegerBits {
			break
		}
	}
	return &InvalidType{}
}

func DefaultIntegerType() Type {
	return &IntegerType{Signed: true, Bits: 32}
}

func LiteralFitsType(value string, typ Type) bool {
	switch t := Underlying(typ).(type) {
	case *IntegerType:
		return numeric.FitsIntegerLiteral(value, t.Bits, t.Signed)
	case *FloatType:
		if numeric.IsFloat(value) {
			return numeric.FitsFloatLiteral(value, t.Bits)
		}
		return numeric.FitsIntegerLiteralInFloat(value, t.Bits)
	case *ByteType:
		return numeric.FitsIntegerLiteral(value, 8, false)
	default:
		return false
	}
}

func IsIntegral(t Type) bool {
	t = Underlying(t)
	switch t.(type) {
	case *IntegerType, *ByteType:
		return true
	default:
		return false
	}
}

func IsArithmetic(t Type) bool {
	t = Underlying(t)
	switch t.(type) {
	case *IntegerType, *ByteType, *FloatType:
		return true
	default:
		return false
	}
}

func IsOrderable(t Type) bool {
	t = Underlying(t)
	switch t.(type) {
	case *IntegerType, *ByteType, *CharType, *FloatType:
		return true
	default:
		return false
	}
}

func IsEquatable(t Type) bool {
	t = Underlying(t)
	switch t.(type) {
	case *IntegerType, *ByteType, *CharType, *FloatType, *BoolType, *CStrType, *RawPtrType, *StringType, *NoneType, *AllocatorType:
		return true
	case *OptionalType:
		return true
	default:
		return false
	}
}

func IsCondition(t Type) bool {
	t = Underlying(t)
	_, ok := t.(*BoolType)
	return ok
}

func IsImplicitCopyType(t Type) bool {
	visiting := make(map[*DefinedType]bool)
	var check func(Type, bool) bool
	check = func(current Type, enumPayload bool) bool {
		if defined, ok := current.(*DefinedType); ok {
			if defined == nil || visiting[defined] {
				return false
			}
			visiting[defined] = true
			defer delete(visiting, defined)
			return check(defined.Underlying, enumPayload)
		}
		switch typ := Underlying(current).(type) {
		case *IntegerType, *ByteType, *CharType, *FloatType, *BoolType, *CStrType, *RawPtrType, *AllocatorType, *NoneType:
			return true
		case *RefType:
			return typ != nil && !typ.Mutable
		case *OptionalType:
			return typ != nil && check(typ.Inner, false)
		case *StructType:
			if typ == nil || !enumPayload {
				return false
			}
			for _, field := range typ.Fields {
				if !check(field.Type, false) {
					return false
				}
			}
			return true
		case *EnumType:
			if typ == nil {
				return false
			}
			for _, variant := range typ.Cases {
				if variant.Payload != nil && !check(variant.Payload, true) {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	return check(t, false)
}

func IsSizedType(t Type) bool {
	visiting := make(map[*DefinedType]bool)
	var check func(Type) bool
	check = func(current Type) bool {
		if current == nil {
			return false
		}
		if defined, ok := current.(*DefinedType); ok {
			if defined == nil || visiting[defined] {
				return false
			}
			visiting[defined] = true
			defer delete(visiting, defined)
			return check(defined.Underlying)
		}
		switch typ := Underlying(current).(type) {
		case *InvalidType, *UnknownType, *InterfaceType:
			return false
		case *IntegerType, *ByteType, *CharType, *FloatType, *BoolType, *CStrType, *StringType, *NoneType, *NamedType, *TypeParameterType, *AllocatorType:
			return true
		case *OwnedPtrType:
			return typ != nil && typ.Target != nil
		case *RefType:
			return typ != nil && typ.Target != nil
		case *RawPtrType:
			return typ != nil
		case *OptionalType:
			return typ != nil && check(typ.Inner)
		case *ArrayType:
			if typ == nil || typ.Elem == nil {
				return false
			}
			if typ.Shape == ArraySlice {
				return false
			}
			return check(typ.Elem)
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if !check(field.Type) {
					return false
				}
			}
			return true
		case *EnumType:
			if typ == nil {
				return false
			}
			for _, variant := range typ.Cases {
				if variant.Payload != nil && !check(variant.Payload) {
					return false
				}
			}
			return true
		case *FuncType:
			if typ == nil {
				return false
			}
			for _, param := range typ.Params {
				if !check(param) {
					return false
				}
			}
			return typ.Return == nil || check(typ.Return)
		default:
			return false
		}
	}
	return check(t)
}

// noCopyType reports whether the type contains owned runtime state, so copies
// are forbidden outright. It is the CopyNever half of the ownership capability
// and is intentionally unexported: consumers classify through
// OwnershipCapabilityOf instead of re-deriving.
func noCopyType(t Type) bool {
	seen := make(map[*DefinedType]bool)
	var check func(Type) bool
	check = func(current Type) bool {
		switch typ := current.(type) {
		case *DefinedType:
			if typ == nil || seen[typ] {
				return false
			}
			seen[typ] = true
			defer delete(seen, typ)
			return check(typ.Underlying)
		}
		switch typ := Underlying(current).(type) {
		case *OwnedPtrType, *StringType, *InterfaceType:
			return true
		case *RefType:
			return typ != nil && typ.Mutable
		case *OptionalType:
			return typ != nil && check(typ.Inner)
		case *ArrayType:
			return typ != nil && (typ.Shape == ArrayOwner || check(typ.Elem))
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if check(field.Type) {
					return true
				}
			}
		case *EnumType:
			if typ == nil {
				return false
			}
			for _, variant := range typ.Cases {
				if variant.Payload != nil && check(variant.Payload) {
					return true
				}
			}
		}
		return false
	}
	return check(t)
}

// IsLowerableType reports whether current backend lowering can represent type.
// Owned pointers and safe references close recursive named composites without expanding storage;
// abstract Self parameters remain semantic-only interface metadata.
func IsLowerableType(t Type) bool {
	visiting := make(map[Type]struct{})
	var check func(Type, bool) bool
	check = func(t Type, throughIndirection bool) bool {
		t = Underlying(t)
		if t == nil {
			return false
		}
		if _, found := visiting[t]; found {
			return throughIndirection
		}
		visiting[t] = struct{}{}
		defer delete(visiting, t)

		switch typ := t.(type) {
		case *IntegerType, *ByteType, *CharType, *FloatType, *BoolType, *CStrType, *StringType, *AllocatorType:
			return true
		case *OwnedPtrType:
			target, ok := PointerTarget(typ)
			return ok && target != nil
		case *RawPtrType:
			return typ != nil
		case *RefType:
			if typ == nil || typ.Target == nil {
				return false
			}
			if _, nested := Underlying(typ.Target).(*RefType); nested {
				return false
			}
			if target, ok := Underlying(typ.Target).(*ArrayType); ok && target != nil && target.Shape == ArraySlice {
				return target.Elem != nil && check(target.Elem, true)
			}
			return check(typ.Target, true)
		case *OptionalType:
			return typ != nil && typ.Inner != nil && check(typ.Inner, throughIndirection)
		case *ArrayType:
			return typ != nil && typ.Shape != ArraySlice && (typ.Shape == ArrayOwner || typ.Len != "") && typ.Elem != nil && check(typ.Elem, throughIndirection)
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if !check(field.Type, throughIndirection) {
					return false
				}
			}
			return true
		case *InterfaceType:
			if typ == nil {
				return false
			}
			for _, method := range typ.Methods {
				if len(method.Params) == 0 {
					return false
				}
				for i, param := range method.Params {
					if i == 0 {
						continue
					}
					if ContainsAbstractSelf(param.Type) || !check(param.Type, throughIndirection) {
						return false
					}
				}
				if method.Return != nil && (ContainsAbstractSelf(method.Return) || !check(method.Return, throughIndirection)) {
					return false
				}
			}
			return true
		case *FuncType:
			if typ == nil {
				return false
			}
			for _, param := range typ.Params {
				if !check(param, throughIndirection) {
					return false
				}
			}
			return typ.Return == nil || check(typ.Return, throughIndirection)
		case *EnumType:
			if typ == nil {
				return false
			}
			for _, variant := range typ.Cases {
				if variant.Payload != nil && !check(variant.Payload, throughIndirection) {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	return check(t, false)
}

// NeedsDrop reports whether normal scope cleanup must destroy runtime-owned
// state reachable through a value. Move-only borrows and plain composites do
// not need destruction; this is intentionally narrower than noCopyType.
func NeedsDrop(t Type) bool {
	seen := make(map[*DefinedType]bool)
	var check func(Type) bool
	check = func(current Type) bool {
		switch typ := current.(type) {
		case *DefinedType:
			if typ == nil || seen[typ] {
				return false
			}
			seen[typ] = true
			defer delete(seen, typ)
			return check(typ.Underlying)
		}
		switch typ := Underlying(current).(type) {
		case *OwnedPtrType, *StringType:
			return true
		case *OptionalType:
			return typ != nil && check(typ.Inner)
		case *ArrayType:
			return typ != nil && (typ.Shape == ArrayOwner || check(typ.Elem))
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if check(field.Type) {
					return true
				}
			}
		case *EnumType:
			if typ == nil {
				return false
			}
			for _, variant := range typ.Cases {
				if variant.Payload != nil && check(variant.Payload) {
					return true
				}
			}
		}
		return false
	}
	return check(t)
}

// CopyClass classifies how a value of a type may be duplicated.
type CopyClass uint8

const (
	// CopyImplicit: a read use copies the value; it is never moved.
	CopyImplicit CopyClass = iota
	// CopyExplicit: a plain use moves the value; an explicit copy
	// operation exists for types that want one.
	CopyExplicit
	// CopyNever: a plain use moves the value; no copy operation exists.
	CopyNever
)

// OwnershipCapability is the canonical classification: how a type duplicates
// (Copy) and whether scope cleanup must destroy it (Drop). One traversal
// answers both, so the two cannot drift apart.
//
// Deliberate asymmetries preserved from the current language semantics:
//   - top-level structs and arrays never copy implicitly (bulk storage),
//     while enum payloads of copyable fields do;
//   - Interface values never copy implicitly but do not yet require source-
//     level drop (owned-interface drop activation is tracked separately);
//   - TypeParameterType is conservatively move-on-use until instantiation-
//     aware capability queries arrive with generic support.
type OwnershipCapability struct {
	Copy CopyClass
	Drop bool
}

// OwnershipCapabilityOf classifies a type's ownership behavior in one traversal.
func OwnershipCapabilityOf(t Type) OwnershipCapability {
	return ownershipCapability(t)
}

// UseKind is the ownership classification of one value use: what happens to
// the value at a specific expression, as decided by the typechecker and
// consumed by ownership and lowering. It is the per-use counterpart of
// OwnershipCapability: the capability constrains which use kinds are legal.
type UseKind uint8

const (
	// UseRead: the value is observed; its owner keeps it.
	UseRead UseKind = iota
	// UseCopy: the value is duplicated; the source keeps it.
	UseCopy
	// UseMove: the value is consumed; the source is dead afterwards.
	UseMove
)
