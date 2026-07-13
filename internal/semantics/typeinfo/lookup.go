package typeinfo

import "slices"

func GetMethodLookupKeys(baseType Type) []string {

	keys := make([]string, 0, 4)
	appendKey := func(typ Type) {
		if typ == nil {
			return
		}
		key := TypeText(typ)
		if key == "" {
			return
		}
		if slices.Contains(keys, key) {
			return
		}
		keys = append(keys, key)
	}
	appendType := func(typ Type) {
		appendKey(typ)
		if underlying := Underlying(typ); underlying != typ {
			appendKey(underlying)
		}
	}
	appendType(baseType)
	if target, ok := PointerTarget(baseType); ok {
		appendType(target)
	}
	if target, _, ok := ReferenceTarget(Underlying(baseType)); ok {
		appendType(target)
	}
	return keys
}

func PointerTarget(t Type) (Type, bool) {
	ptr, ok := t.(*OwnedPtrType)
	if ok && ptr != nil && ptr.Target != nil {
		return ptr.Target, true
	}
	return nil, false
}

func ReferenceTarget(t Type) (target Type, mutable bool, ok bool) {
	ref, ok := t.(*RefType)
	if !ok || ref == nil || ref.Target == nil {
		return nil, false, false
	}
	return ref.Target, ref.Mutable, true
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

// LookupStructField centralizes field search so checker and lowerer agree on
// struct layout. Checker needs the field type for validation; lowerer needs
// the same field index to emit field access.
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
