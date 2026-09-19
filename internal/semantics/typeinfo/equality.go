package typeinfo

import "slices"

func (*InvalidType) sameType(other Type) bool {
	_, ok := other.(*InvalidType)
	return ok
}

func (*UnknownType) sameType(other Type) bool {
	_, ok := other.(*UnknownType)
	return ok
}

func (t *IntegerType) sameType(other Type) bool {
	right, ok := other.(*IntegerType)
	return ok && t != nil && right != nil && t.Signed == right.Signed && t.Bits == right.Bits
}

func (*ByteType) sameType(other Type) bool {
	_, ok := other.(*ByteType)
	return ok
}

func (*CharType) sameType(other Type) bool {
	_, ok := other.(*CharType)
	return ok
}

func (t *FloatType) sameType(other Type) bool {
	right, ok := other.(*FloatType)
	return ok && t != nil && right != nil && t.Bits == right.Bits
}

func (*BoolType) sameType(other Type) bool {
	_, ok := other.(*BoolType)
	return ok
}

func (*CStrType) sameType(other Type) bool {
	_, ok := other.(*CStrType)
	return ok
}

func (*StringType) sameType(other Type) bool {
	_, ok := other.(*StringType)
	return ok
}

func (*NoneType) sameType(other Type) bool {
	_, ok := other.(*NoneType)
	return ok
}

func (*AllocatorType) sameType(other Type) bool {
	_, ok := other.(*AllocatorType)
	return ok
}

func (t *NamedType) sameType(other Type) bool {
	right, ok := other.(*NamedType)
	return ok && t != nil && right != nil && t.Name == right.Name
}

func (t *TypeParameterType) sameType(other Type) bool {
	right, ok := other.(*TypeParameterType)
	return ok && t != nil && right != nil &&
		t.OwnerIdentity == right.OwnerIdentity && t.Index == right.Index
}

func (*DefinedType) sameType(Type) bool {
	// SameType handles nominal identity before unwrapping complete definitions.
	// Distinct incomplete definitions have no structural evidence to compare.
	return false
}

func (t *OwnedPtrType) sameType(other Type) bool {
	right, ok := other.(*OwnedPtrType)
	return ok && t != nil && right != nil && SameType(t.Target, right.Target)
}

func (*RawPtrType) sameType(other Type) bool {
	_, ok := other.(*RawPtrType)
	return ok
}

func (t *RefType) sameType(other Type) bool {
	right, ok := other.(*RefType)
	return ok && t != nil && right != nil &&
		t.Mutable == right.Mutable && SameType(t.Target, right.Target)
}

func (t *OptionalType) sameType(other Type) bool {
	right, ok := other.(*OptionalType)
	return ok && t != nil && right != nil && SameType(t.Inner, right.Inner)
}

func (t *ArrayType) sameType(other Type) bool {
	right, ok := other.(*ArrayType)
	return ok && t != nil && right != nil &&
		t.Len == right.Len && t.Shape == right.Shape && SameType(t.Elem, right.Elem)
}

func (t *FuncType) sameType(other Type) bool {
	right, ok := other.(*FuncType)
	if !ok || t == nil || right == nil || len(t.Params) != len(right.Params) {
		return false
	}
	for index := range t.Params {
		if !SameType(t.Params[index], right.Params[index]) {
			return false
		}
	}
	return SameType(t.Return, right.Return) && sameReturnOriginContract(t.ReturnOrigins, right.ReturnOrigins)
}

// sameType compares structural fields by name because source struct identity is
// independent of declaration order. Nominal identity is handled by SameType.
func (t *StructType) sameType(other Type) bool {
	right, ok := other.(*StructType)
	if !ok || t == nil || right == nil || len(t.Fields) != len(right.Fields) {
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
	for _, field := range t.Fields {
		rightType, found := rightFields[field.Name]
		if !found || !SameType(field.Type, rightType) {
			return false
		}
		delete(rightFields, field.Name)
	}
	return len(rightFields) == 0
}

func (t *InterfaceType) sameType(other Type) bool {
	right, ok := other.(*InterfaceType)
	if !ok || t == nil || right == nil || len(t.Methods) != len(right.Methods) {
		return false
	}
	for index, method := range t.Methods {
		otherMethod := right.Methods[index]
		if method.Name != otherMethod.Name || len(method.Params) != len(otherMethod.Params) {
			return false
		}
		for parameterIndex := range method.Params {
			if !SameType(method.Params[parameterIndex].Type, otherMethod.Params[parameterIndex].Type) {
				return false
			}
		}
		if !SameType(method.Return, otherMethod.Return) ||
			!sameReturnOriginContract(method.ReturnOrigins, otherMethod.ReturnOrigins) {
			return false
		}
	}
	return true
}

func (t *EnumType) sameType(other Type) bool {
	right, ok := other.(*EnumType)
	if !ok || t == nil || right == nil || len(t.Cases) != len(right.Cases) {
		return false
	}
	for index, variant := range t.Cases {
		otherVariant := right.Cases[index]
		if variant.Name != otherVariant.Name || !SameType(variant.Payload, otherVariant.Payload) {
			return false
		}
	}
	return true
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
