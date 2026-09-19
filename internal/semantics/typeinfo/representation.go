package typeinfo

import "compiler/pkg/typednil"

// Sizedness and lowerability deliberately use separate query state. Sizedness
// rejects recursive inline storage by declaration identity; lowerability tracks
// underlying representations and accepts cycles reached through indirection.
// Sharing one walker would hide these different invariants.

// IsSizedType reports whether a type has finite inline storage.
func IsSizedType(t Type) bool {
	return (&sizeQuery{visiting: make(map[*DefinedType]bool)}).check(t)
}

type sizeQuery struct {
	visiting map[*DefinedType]bool
}

func (q *sizeQuery) check(t Type) bool {
	if t == nil {
		return false
	}
	return t.isSized(q)
}

func (q *sizeQuery) allChildren(t Type) bool {
	if typednil.IsNil(t) {
		return false
	}
	return ForEachChild(t, func(child TypeChild) bool {
		return q.check(child.Type)
	})
}

func (*InvalidType) isSized(*sizeQuery) bool       { return false }
func (*UnknownType) isSized(*sizeQuery) bool       { return false }
func (*IntegerType) isSized(*sizeQuery) bool       { return true }
func (*ByteType) isSized(*sizeQuery) bool          { return true }
func (*CharType) isSized(*sizeQuery) bool          { return true }
func (*FloatType) isSized(*sizeQuery) bool         { return true }
func (*BoolType) isSized(*sizeQuery) bool          { return true }
func (*CStrType) isSized(*sizeQuery) bool          { return true }
func (*StringType) isSized(*sizeQuery) bool        { return true }
func (*NoneType) isSized(*sizeQuery) bool          { return true }
func (*AllocatorType) isSized(*sizeQuery) bool     { return true }
func (*NamedType) isSized(*sizeQuery) bool         { return true }
func (*TypeParameterType) isSized(*sizeQuery) bool { return true }
func (*InterfaceType) isSized(*sizeQuery) bool     { return false }

func (t *DefinedType) isSized(q *sizeQuery) bool {
	if t == nil || q.visiting[t] {
		return false
	}
	q.visiting[t] = true
	defer delete(q.visiting, t)

	result := false
	ForEachChild(t, func(child TypeChild) bool {
		if child.Relation != TypeChildUnderlying {
			return true
		}
		result = q.check(child.Type)
		return false
	})
	return result
}

func (t *OwnedPtrType) isSized(*sizeQuery) bool { return t != nil && t.Target != nil }
func (t *RawPtrType) isSized(*sizeQuery) bool   { return t != nil }
func (t *RefType) isSized(*sizeQuery) bool      { return t != nil && t.Target != nil }

func (t *OptionalType) isSized(q *sizeQuery) bool {
	if t == nil {
		return false
	}
	result := false
	ForEachChild(t, func(child TypeChild) bool {
		result = q.check(child.Type)
		return false
	})
	return result
}

func (t *ArrayType) isSized(q *sizeQuery) bool {
	if t == nil || t.Elem == nil || t.Shape == ArraySlice {
		return false
	}
	result := false
	ForEachChild(t, func(child TypeChild) bool {
		result = q.check(child.Type)
		return false
	})
	return result
}

func (t *FuncType) isSized(q *sizeQuery) bool   { return q.allChildren(t) }
func (t *StructType) isSized(q *sizeQuery) bool { return q.allChildren(t) }
func (t *EnumType) isSized(q *sizeQuery) bool   { return q.allChildren(t) }

// IsLowerableType reports whether every compiler lowering can currently
// materialize a type accepted by source semantics.
func IsLowerableType(t Type) bool {
	return (&lowerQuery{visiting: make(map[Type]struct{})}).check(t, false)
}

type lowerQuery struct {
	visiting map[Type]struct{}
}

func (q *lowerQuery) check(t Type, throughIndirection bool) bool {
	t = Underlying(t)
	if t == nil {
		return false
	}
	if _, found := q.visiting[t]; found {
		return throughIndirection
	}
	q.visiting[t] = struct{}{}
	defer delete(q.visiting, t)
	return t.isLowerable(q, throughIndirection)
}

func (q *lowerQuery) allChildren(t Type, throughIndirection bool) bool {
	if typednil.IsNil(t) {
		return false
	}
	return ForEachChild(t, func(child TypeChild) bool {
		return q.check(child.Type, throughIndirection)
	})
}

func (*InvalidType) isLowerable(*lowerQuery, bool) bool       { return false }
func (*UnknownType) isLowerable(*lowerQuery, bool) bool       { return false }
func (*IntegerType) isLowerable(*lowerQuery, bool) bool       { return true }
func (*ByteType) isLowerable(*lowerQuery, bool) bool          { return true }
func (*CharType) isLowerable(*lowerQuery, bool) bool          { return true }
func (*FloatType) isLowerable(*lowerQuery, bool) bool         { return true }
func (*BoolType) isLowerable(*lowerQuery, bool) bool          { return true }
func (*CStrType) isLowerable(*lowerQuery, bool) bool          { return true }
func (*StringType) isLowerable(*lowerQuery, bool) bool        { return true }
func (*NoneType) isLowerable(*lowerQuery, bool) bool          { return false }
func (*AllocatorType) isLowerable(*lowerQuery, bool) bool     { return true }
func (*NamedType) isLowerable(*lowerQuery, bool) bool         { return false }
func (*TypeParameterType) isLowerable(*lowerQuery, bool) bool { return false }
func (*DefinedType) isLowerable(*lowerQuery, bool) bool       { return false }

func (t *OwnedPtrType) isLowerable(*lowerQuery, bool) bool {
	target, ok := PointerTarget(t)
	return ok && target != nil
}

func (t *RawPtrType) isLowerable(*lowerQuery, bool) bool { return t != nil }

func (t *RefType) isLowerable(q *lowerQuery, _ bool) bool {
	if t == nil || t.Target == nil {
		return false
	}
	if _, nested := Underlying(t.Target).(*RefType); nested {
		return false
	}
	if target, ok := Underlying(t.Target).(*ArrayType); ok && target != nil && target.Shape == ArraySlice {
		return target.Elem != nil && q.check(target.Elem, true)
	}
	return q.check(t.Target, true)
}

func (t *OptionalType) isLowerable(q *lowerQuery, throughIndirection bool) bool {
	if t == nil || t.Inner == nil {
		return false
	}
	result := false
	ForEachChild(t, func(child TypeChild) bool {
		result = q.check(child.Type, throughIndirection)
		return false
	})
	return result
}

func (t *ArrayType) isLowerable(q *lowerQuery, throughIndirection bool) bool {
	if t == nil || t.Shape == ArraySlice || (t.Shape != ArrayOwner && t.Len == "") || t.Elem == nil {
		return false
	}
	result := false
	ForEachChild(t, func(child TypeChild) bool {
		result = q.check(child.Type, throughIndirection)
		return false
	})
	return result
}

func (t *FuncType) isLowerable(q *lowerQuery, throughIndirection bool) bool {
	return q.allChildren(t, throughIndirection)
}

func (t *StructType) isLowerable(q *lowerQuery, throughIndirection bool) bool {
	return q.allChildren(t, throughIndirection)
}

func (t *EnumType) isLowerable(q *lowerQuery, throughIndirection bool) bool {
	return q.allChildren(t, throughIndirection)
}

func (t *InterfaceType) isLowerable(q *lowerQuery, throughIndirection bool) bool {
	if t == nil {
		return false
	}
	for _, method := range t.Methods {
		if len(method.Params) == 0 {
			return false
		}
	}
	return ForEachChild(t, func(child TypeChild) bool {
		if child.Relation == TypeChildMethodReceiver {
			return true
		}
		return !ContainsAbstractSelf(child.Type) && q.check(child.Type, throughIndirection)
	})
}
