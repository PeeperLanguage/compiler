package typeinfo

import "slices"

func (*InvalidType) isSameType(other Type) bool {
	_, ok := other.(*InvalidType)
	return ok
}

func (*UnknownType) isSameType(other Type) bool {
	_, ok := other.(*UnknownType)
	return ok
}

func (t *IntegerType) isSameType(other Type) bool {
	right, ok := other.(*IntegerType)
	return ok && t != nil && right != nil && t.IsSigned == right.IsSigned && t.Bits == right.Bits
}

func (*ByteType) isSameType(other Type) bool {
	_, ok := other.(*ByteType)
	return ok
}

func (*CharType) isSameType(other Type) bool {
	_, ok := other.(*CharType)
	return ok
}

func (t *FloatType) isSameType(other Type) bool {
	right, ok := other.(*FloatType)
	return ok && t != nil && right != nil && t.Bits == right.Bits
}

func (*BoolType) isSameType(other Type) bool {
	_, ok := other.(*BoolType)
	return ok
}

func (*CStrType) isSameType(other Type) bool {
	_, ok := other.(*CStrType)
	return ok
}

func (*StringType) isSameType(other Type) bool {
	_, ok := other.(*StringType)
	return ok
}

func (*NoneType) isSameType(other Type) bool {
	_, ok := other.(*NoneType)
	return ok
}

func (*AllocatorType) isSameType(other Type) bool {
	_, ok := other.(*AllocatorType)
	return ok
}

func (t *NamedType) isSameType(other Type) bool {
	right, ok := other.(*NamedType)
	return ok && t != nil && right != nil && t.Name == right.Name
}

func (t *TypeParameterType) isSameType(other Type) bool {
	right, ok := other.(*TypeParameterType)
	return ok && t != nil && right != nil &&
		t.OwnerIdentity == right.OwnerIdentity && t.Index == right.Index
}

func (*DefinedType) isSameType(Type) bool {
	// SameType handles nominal identity before unwrapping complete definitions.
	// Distinct incomplete definitions have no structural evidence to compare.
	return false
}

func (t *OwnedPtrType) isSameType(other Type) bool {
	right, ok := other.(*OwnedPtrType)
	return ok && t != nil && right != nil && IsSameType(t.Target, right.Target)
}

func (*RawPtrType) isSameType(other Type) bool {
	_, ok := other.(*RawPtrType)
	return ok
}

func (t *RefType) isSameType(other Type) bool {
	right, ok := other.(*RefType)
	return ok && t != nil && right != nil &&
		t.IsMutable == right.IsMutable && IsSameType(t.Target, right.Target)
}

func (t *OptionalType) isSameType(other Type) bool {
	right, ok := other.(*OptionalType)
	return ok && t != nil && right != nil && IsSameType(t.Inner, right.Inner)
}

func (t *ArrayType) isSameType(other Type) bool {
	right, ok := other.(*ArrayType)
	return ok && t != nil && right != nil &&
		t.Len == right.Len && t.Shape == right.Shape && IsSameType(t.Elem, right.Elem)
}

func (t *FuncType) isSameType(other Type) bool {
	right, ok := other.(*FuncType)
	if !ok || t == nil || right == nil || len(t.Params) != len(right.Params) {
		return false
	}
	for index := range t.Params {
		if !IsSameType(t.Params[index], right.Params[index]) {
			return false
		}
	}
	return IsSameType(t.Return, right.Return) && returnOriginContractsEqual(t.ReturnOrigins, right.ReturnOrigins)
}

// Nominal identity is handled by SameType.
func (t *StructType) isSameType(other Type) bool {
	right, ok := other.(*StructType)
	if !ok || t == nil || right == nil || len(t.Fields) != len(right.Fields) {
		return false
	}
	seen := make(map[string]struct{}, len(t.Fields))
	for index, field := range t.Fields {
		otherField := right.Fields[index]
		if field.Name == "" || field.Name != otherField.Name || !IsSameType(field.Type, otherField.Type) {
			return false
		}
		if _, exists := seen[field.Name]; exists {
			return false
		}
		seen[field.Name] = struct{}{}
	}
	return true
}

func (t *InterfaceType) isSameType(other Type) bool {
	right, ok := other.(*InterfaceType)
	if !ok || t == nil || right == nil || len(t.Methods) != len(right.Methods) {
		return false
	}
	for index, method := range t.Methods {
		otherMethod := right.Methods[index]
		if method.Name != otherMethod.Name || method.Receiver != otherMethod.Receiver ||
			len(method.Params) != len(otherMethod.Params) {
			return false
		}
		for parameterIndex := range method.Params {
			if !IsSameType(method.Params[parameterIndex].Type, otherMethod.Params[parameterIndex].Type) {
				return false
			}
		}
		if !IsSameType(method.Return, otherMethod.Return) ||
			!returnOriginContractsEqual(method.ReturnOrigins, otherMethod.ReturnOrigins) {
			return false
		}
	}
	return true
}

func (t *EnumType) isSameType(other Type) bool {
	right, ok := other.(*EnumType)
	if !ok || t == nil || right == nil || len(t.Cases) != len(right.Cases) {
		return false
	}
	for index, variant := range t.Cases {
		otherVariant := right.Cases[index]
		if variant.Name != otherVariant.Name || !IsSameType(variant.Payload, otherVariant.Payload) {
			return false
		}
	}
	return true
}

func returnOriginContractsEqual(left, right *ReturnOriginContract) bool {
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
