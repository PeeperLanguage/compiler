package typeinfo

// TypeChildRelation describes why one semantic type contains another. The
// relation is structural evidence, not an analysis result: ownership, sizing,
// lowerability, substitution, and future queries may interpret the same child
// differently while sharing one canonical declaration of where that child is.
type TypeChildRelation uint8

const (
	TypeChildUnderlying TypeChildRelation = iota
	TypeChildOwnedTarget
	TypeChildBorrowedTarget
	TypeChildOptionalPayload
	TypeChildArrayElement
	TypeChildStructField
	TypeChildEnumPayload
	TypeChildMethodReceiver
	TypeChildCallableParameter
	TypeChildCallableReturn
)

// TypeChild is one immediate semantic-type edge. A new composite Type must
// expose its children here through Type.forEachChild; recursive consumers must
// not rediscover fields with their own type switches.
type TypeChild struct {
	Type     Type
	Relation TypeChildRelation
}

// ForEachChild visits the immediate semantic children of typ in source/semantic
// order. It returns false when yield asks traversal to stop.
//
// This is the semantic-type equivalent of ast.Node.forEachChild: it owns type
// structure once while leaving recursion policy to the consumer. Cycle rules
// deliberately stay with each analysis because sizedness, lowerability, and
// ownership do not assign the same meaning to recursive edges.
func ForEachChild(typ Type, yield func(TypeChild) bool) bool {
	if typ == nil || yield == nil {
		return true
	}
	return typ.forEachChild(yield)
}

func (*InvalidType) forEachChild(func(TypeChild) bool) bool       { return true }
func (*UnknownType) forEachChild(func(TypeChild) bool) bool       { return true }
func (*IntegerType) forEachChild(func(TypeChild) bool) bool       { return true }
func (*ByteType) forEachChild(func(TypeChild) bool) bool          { return true }
func (*CharType) forEachChild(func(TypeChild) bool) bool          { return true }
func (*FloatType) forEachChild(func(TypeChild) bool) bool         { return true }
func (*BoolType) forEachChild(func(TypeChild) bool) bool          { return true }
func (*CStrType) forEachChild(func(TypeChild) bool) bool          { return true }
func (*StringType) forEachChild(func(TypeChild) bool) bool        { return true }
func (*NoneType) forEachChild(func(TypeChild) bool) bool          { return true }
func (*AllocatorType) forEachChild(func(TypeChild) bool) bool     { return true }
func (*NamedType) forEachChild(func(TypeChild) bool) bool         { return true }
func (*TypeParameterType) forEachChild(func(TypeChild) bool) bool { return true }
func (*RawPtrType) forEachChild(func(TypeChild) bool) bool        { return true }

func (t *DefinedType) forEachChild(yield func(TypeChild) bool) bool {
	if t == nil {
		return true
	}
	return yieldTypeChild(yield, t.Underlying, TypeChildUnderlying)
}

func (t *OwnedPtrType) forEachChild(yield func(TypeChild) bool) bool {
	if t == nil {
		return true
	}
	return yieldTypeChild(yield, t.Target, TypeChildOwnedTarget)
}

func (t *RefType) forEachChild(yield func(TypeChild) bool) bool {
	if t == nil {
		return true
	}
	return yieldTypeChild(yield, t.Target, TypeChildBorrowedTarget)
}

func (t *OptionalType) forEachChild(yield func(TypeChild) bool) bool {
	if t == nil {
		return true
	}
	return yieldTypeChild(yield, t.Inner, TypeChildOptionalPayload)
}

func (t *ArrayType) forEachChild(yield func(TypeChild) bool) bool {
	if t == nil {
		return true
	}
	return yieldTypeChild(yield, t.Elem, TypeChildArrayElement)
}

func (t *FuncType) forEachChild(yield func(TypeChild) bool) bool {
	if t == nil {
		return true
	}
	for _, param := range t.Params {
		if param != nil && !yield(TypeChild{Type: param, Relation: TypeChildCallableParameter}) {
			return false
		}
	}
	return t.Return == nil || yield(TypeChild{Type: t.Return, Relation: TypeChildCallableReturn})
}

func (t *StructType) forEachChild(yield func(TypeChild) bool) bool {
	if t == nil {
		return true
	}
	for _, field := range t.Fields {
		if field.Type != nil && !yield(TypeChild{Type: field.Type, Relation: TypeChildStructField}) {
			return false
		}
	}
	return true
}

func (t *InterfaceType) forEachChild(yield func(TypeChild) bool) bool {
	if t == nil {
		return true
	}
	for _, method := range t.Methods {
		for index, param := range method.Params {
			relation := TypeChildCallableParameter
			if index == 0 {
				relation = TypeChildMethodReceiver
			}
			if param.Type != nil && !yield(TypeChild{Type: param.Type, Relation: relation}) {
				return false
			}
		}
		if method.Return != nil && !yield(TypeChild{Type: method.Return, Relation: TypeChildCallableReturn}) {
			return false
		}
	}
	return true
}

func (t *EnumType) forEachChild(yield func(TypeChild) bool) bool {
	if t == nil {
		return true
	}
	for _, variant := range t.Cases {
		if variant.Payload != nil && !yield(TypeChild{Type: variant.Payload, Relation: TypeChildEnumPayload}) {
			return false
		}
	}
	return true
}

func yieldTypeChild(yield func(TypeChild) bool, child Type, relation TypeChildRelation) bool {
	if child == nil {
		return true
	}
	return yield(TypeChild{Type: child, Relation: relation})
}
