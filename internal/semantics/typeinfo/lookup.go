package typeinfo

// ReceiverIdentity returns the nominal declaration identity whose method set
// owns typ. Pointer/reference carriers and transparent aliases do not create
// separate method namespaces.
func ReceiverIdentity(typ Type) (string, bool) {
	target, ok := ReceiverTarget(typ)
	if !ok || target == nil {
		return "", false
	}
	target = Unalias(target)
	defined, ok := target.(*DefinedType)
	if !ok || defined == nil || defined.Identity == "" {
		return "", false
	}
	return defined.Identity, true
}

func PointerTarget(t Type) (Type, bool) {
	ptr, ok := t.(*OwnedPtrType)
	if ok && ptr != nil && ptr.Target != nil {
		return ptr.Target, true
	}
	return nil, false
}

func ReferenceTarget(t Type) (target Type, isMutable bool, ok bool) {
	ref, ok := t.(*RefType)
	if !ok || ref == nil || ref.Target == nil {
		return nil, false, false
	}
	return ref.Target, ref.IsMutable, true
}

// ReferenceValueTarget recognizes direct references and reference values made
// nullable through optional wrappers. ReferenceTarget stays direct so pointer,
// receiver, and method lookup rules do not treat optionals as transparent.
func ReferenceValueTarget(t Type) (target Type, isMutable bool, ok bool) {
	for {
		t = Underlying(t)
		optional, optionalValue := t.(*OptionalType)
		if !optionalValue || optional == nil {
			return ReferenceTarget(t)
		}
		t = optional.Inner
	}
}

// ReceiverTarget returns concrete type whose method set owns receiver.
func ReceiverTarget(t Type) (Type, bool) {
	if target, ok := PointerTarget(t); ok {
		return target, true
	}
	if target, _, ok := ReferenceTarget(Underlying(t)); ok {
		return target, true
	}
	if t == nil {
		return nil, false
	}
	return t, true
}

// InterfaceTypeOf recognizes interface declarations and their borrowed or
// owned fat-pointer carriers. Raw pointers never carry interface metadata.
func InterfaceTypeOf(t Type) (*InterfaceType, bool) {
	if owner, ok := Underlying(t).(*OwnedPtrType); ok && owner != nil {
		t = owner.Target
	} else if target, _, ok := ReferenceTarget(Underlying(t)); ok {
		t = target
	}
	iface, ok := Underlying(t).(*InterfaceType)
	return iface, ok && iface != nil
}

// LookupStructField centralizes semantic field search. Typechecking publishes
// the selected slot and type for lowering rather than asking lowering to search again.
func LookupStructField(baseType Type, name string) (field Field, index int, ok bool) {
	if baseType == nil || name == "" {
		return Field{}, -1, false
	}
	if target, ptrOK := PointerTarget(baseType); ptrOK {
		baseType = target
	} else if target, _, refOK := ReferenceTarget(Underlying(baseType)); refOK {
		baseType = target
	}
	strct, ok := Underlying(baseType).(*StructType)
	if !ok || strct == nil {
		return Field{}, -1, false
	}
	for i, candidate := range strct.Fields {
		if candidate.Name == name {
			return candidate, i, true
		}
	}
	return Field{}, -1, false
}

// LookupVariantCase centralizes source-case lookup over canonical descriptors.
func LookupVariantCase(descriptor VariantDescriptor, name string) (variant VariantCase, index int, ok bool) {
	for index, variant := range descriptor.Cases {
		if variant.Name == name {
			return variant, index, true
		}
	}
	return VariantCase{}, -1, false
}
