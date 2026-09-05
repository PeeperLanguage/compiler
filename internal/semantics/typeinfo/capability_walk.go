package typeinfo

// ownershipShapeKind describes how a semantic type contributes copy/drop
// behavior. It is part of the sealed Type contract: a new type must declare
// both its structural children and how ownership composes across them.
type ownershipShapeKind uint8

const (
	ownershipLeaf ownershipShapeKind = iota
	ownershipAlias
	ownershipOptional
	ownershipBulk
	ownershipStruct
	ownershipEnum
)

type ownershipShape struct {
	kind  ownershipShapeKind
	facts capabilityFacts
}

func leafOwnership(implicitCopy, noCopy, drop bool) ownershipShape {
	return ownershipShape{kind: ownershipLeaf, facts: capabilityFacts{
		implicitCopy: implicitCopy,
		noCopy:       noCopy,
		drop:         drop,
	}}
}

func (*InvalidType) ownershipShape() ownershipShape       { return leafOwnership(false, false, false) }
func (*UnknownType) ownershipShape() ownershipShape       { return leafOwnership(false, false, false) }
func (*IntegerType) ownershipShape() ownershipShape       { return leafOwnership(true, false, false) }
func (*ByteType) ownershipShape() ownershipShape          { return leafOwnership(true, false, false) }
func (*CharType) ownershipShape() ownershipShape          { return leafOwnership(true, false, false) }
func (*FloatType) ownershipShape() ownershipShape         { return leafOwnership(true, false, false) }
func (*BoolType) ownershipShape() ownershipShape          { return leafOwnership(true, false, false) }
func (*CStrType) ownershipShape() ownershipShape          { return leafOwnership(true, false, false) }
func (*StringType) ownershipShape() ownershipShape        { return leafOwnership(false, true, true) }
func (*NoneType) ownershipShape() ownershipShape          { return leafOwnership(true, false, false) }
func (*AllocatorType) ownershipShape() ownershipShape     { return leafOwnership(true, false, false) }
func (*NamedType) ownershipShape() ownershipShape         { return leafOwnership(false, false, false) }
func (*TypeParameterType) ownershipShape() ownershipShape { return leafOwnership(false, false, false) }
func (*RawPtrType) ownershipShape() ownershipShape        { return leafOwnership(true, false, false) }
func (*FuncType) ownershipShape() ownershipShape          { return leafOwnership(false, false, false) }

func (*DefinedType) ownershipShape() ownershipShape {
	return ownershipShape{kind: ownershipAlias}
}

// An owned pointer owns its allocation as one value. The target remains a
// structural child for type queries, but copy/drop do not recursively inherit
// from the pointee because destroying the pointer is already the ownership act.
func (*OwnedPtrType) ownershipShape() ownershipShape { return leafOwnership(false, true, true) }

func (t *RefType) ownershipShape() ownershipShape {
	if t == nil {
		return leafOwnership(false, false, false)
	}
	return leafOwnership(!t.Mutable, t.Mutable, false)
}

func (*OptionalType) ownershipShape() ownershipShape {
	return ownershipShape{kind: ownershipOptional}
}

func (t *ArrayType) ownershipShape() ownershipShape {
	if t != nil && t.Shape == ArrayOwner {
		return leafOwnership(false, true, true)
	}
	return ownershipShape{kind: ownershipBulk}
}

func (*StructType) ownershipShape() ownershipShape {
	return ownershipShape{kind: ownershipStruct}
}

func (*InterfaceType) ownershipShape() ownershipShape {
	// Owned-interface drop activation is tracked separately by ownership.
	return leafOwnership(false, true, false)
}

func (*EnumType) ownershipShape() ownershipShape {
	return ownershipShape{kind: ownershipEnum}
}

// ownershipCapability answers copy class and drop obligation in one generic
// traversal. Type implementations declare structure and composition locally;
// the walker never names a concrete Type kind.
func ownershipCapability(t Type) OwnershipCapability {
	visiting := make(map[Type]bool)

	var walk func(Type, bool) capabilityFacts
	walk = func(current Type, enumPayload bool) capabilityFacts {
		if current == nil || isNilType(current) || visiting[current] {
			return capabilityFacts{}
		}
		shape := current.ownershipShape()
		if shape.kind == ownershipLeaf {
			return shape.facts
		}

		visiting[current] = true
		defer delete(visiting, current)

		switch shape.kind {
		case ownershipAlias:
			facts := capabilityFacts{}
			ForEachChild(current, func(child TypeChild) bool {
				if ownsChild(child.Relation) {
					facts = walk(child.Type, enumPayload)
					return false
				}
				return true
			})
			return facts
		case ownershipOptional:
			facts := capabilityFacts{}
			ForEachChild(current, func(child TypeChild) bool {
				if ownsChild(child.Relation) {
					// Optional payloads are ordinary value storage; being nested in an
					// enum does not turn bulk payload storage into implicit-copy data.
					facts = walk(child.Type, false)
					return false
				}
				return true
			})
			return facts
		case ownershipBulk:
			facts := capabilityFacts{}
			ForEachChild(current, func(child TypeChild) bool {
				if ownsChild(child.Relation) {
					inner := walk(child.Type, false)
					// Fixed/slice array values never copy implicitly as bulk storage,
					// but a non-copy/drop element still propagates through the value.
					facts.noCopy = inner.noCopy
					facts.drop = inner.drop
					return false
				}
				return true
			})
			return facts
		case ownershipStruct:
			facts := capabilityFacts{implicitCopy: enumPayload}
			ForEachChild(current, func(child TypeChild) bool {
				if !ownsChild(child.Relation) {
					return true
				}
				inner := walk(child.Type, false)
				facts.implicitCopy = facts.implicitCopy && inner.implicitCopy
				facts.noCopy = facts.noCopy || inner.noCopy
				facts.drop = facts.drop || inner.drop
				return true
			})
			return facts
		case ownershipEnum:
			facts := capabilityFacts{implicitCopy: true}
			ForEachChild(current, func(child TypeChild) bool {
				if !ownsChild(child.Relation) {
					return true
				}
				inner := walk(child.Type, true)
				facts.implicitCopy = facts.implicitCopy && inner.implicitCopy
				facts.noCopy = facts.noCopy || inner.noCopy
				facts.drop = facts.drop || inner.drop
				return true
			})
			return facts
		default:
			return capabilityFacts{}
		}
	}

	facts := walk(t, false)
	switch {
	case facts.implicitCopy:
		return OwnershipCapability{Copy: CopyImplicit, Drop: facts.drop}
	case facts.noCopy:
		return OwnershipCapability{Copy: CopyNever, Drop: facts.drop}
	default:
		return OwnershipCapability{Copy: CopyExplicit, Drop: facts.drop}
	}
}

func ownsChild(relation TypeChildRelation) bool {
	switch relation {
	case TypeChildUnderlying, TypeChildOwnedTarget, TypeChildOptionalPayload,
		TypeChildArrayElement, TypeChildStructField, TypeChildEnumPayload:
		return true
	default:
		return false
	}
}

type capabilityFacts struct {
	implicitCopy bool
	noCopy       bool
	drop         bool
}
