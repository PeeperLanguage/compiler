package typeinfo

import "reflect"

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

// isNilType handles a typed-nil pointer stored in the Type interface without
// enumerating concrete type kinds. Type is sealed to this package and all
// current implementations are pointer types, but the kind guard keeps this
// helper correct if a value implementation is ever introduced.
func isNilType(typ Type) bool {
	if typ == nil {
		return true
	}
	value := reflect.ValueOf(typ)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func noTypeChildren(func(TypeChild) bool) bool { return true }

func (*InvalidType) forEachChild(yield func(TypeChild) bool) bool       { return noTypeChildren(yield) }
func (*UnknownType) forEachChild(yield func(TypeChild) bool) bool       { return noTypeChildren(yield) }
func (*IntegerType) forEachChild(yield func(TypeChild) bool) bool       { return noTypeChildren(yield) }
func (*ByteType) forEachChild(yield func(TypeChild) bool) bool          { return noTypeChildren(yield) }
func (*CharType) forEachChild(yield func(TypeChild) bool) bool          { return noTypeChildren(yield) }
func (*FloatType) forEachChild(yield func(TypeChild) bool) bool         { return noTypeChildren(yield) }
func (*BoolType) forEachChild(yield func(TypeChild) bool) bool          { return noTypeChildren(yield) }
func (*CStrType) forEachChild(yield func(TypeChild) bool) bool          { return noTypeChildren(yield) }
func (*StringType) forEachChild(yield func(TypeChild) bool) bool        { return noTypeChildren(yield) }
func (*NoneType) forEachChild(yield func(TypeChild) bool) bool          { return noTypeChildren(yield) }
func (*AllocatorType) forEachChild(yield func(TypeChild) bool) bool     { return noTypeChildren(yield) }
func (*NamedType) forEachChild(yield func(TypeChild) bool) bool         { return noTypeChildren(yield) }
func (*TypeParameterType) forEachChild(yield func(TypeChild) bool) bool { return noTypeChildren(yield) }
func (*RawPtrType) forEachChild(yield func(TypeChild) bool) bool        { return noTypeChildren(yield) }

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
