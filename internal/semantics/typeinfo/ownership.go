package typeinfo

import "compiler/pkg/typednil"

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
	Copy      CopyClass
	NeedsDrop bool
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

// OwnershipCapabilityOf classifies a type's ownership behavior in one traversal.
func OwnershipCapabilityOf(t Type) OwnershipCapability {
	return (&ownershipQuery{visiting: make(map[Type]bool)}).check(t, false)
}

type ownershipQuery struct {
	visiting map[Type]bool
}

func (q *ownershipQuery) check(t Type, enumPayload bool) OwnershipCapability {
	if t == nil || typednil.IsNil(t) || q.visiting[t] {
		return OwnershipCapability{Copy: CopyExplicit}
	}
	q.visiting[t] = true
	defer delete(q.visiting, t)
	return t.ownership(q, enumPayload)
}

// mergeOwnership combines stored children using CopyClass severity and one
// shared drop obligation. Composite types choose their own initial copy class.
func mergeOwnership(base, child OwnershipCapability) OwnershipCapability {
	if child.Copy > base.Copy {
		base.Copy = child.Copy
	}
	base.NeedsDrop = base.NeedsDrop || child.NeedsDrop
	return base
}

func (*InvalidType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyExplicit}
}
func (*UnknownType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyExplicit}
}
func (*IntegerType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyImplicit}
}
func (*ByteType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyImplicit}
}
func (*CharType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyImplicit}
}
func (*FloatType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyImplicit}
}
func (*BoolType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyImplicit}
}
func (*CStrType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyImplicit}
}
func (*StringType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyNever, NeedsDrop: true}
}
func (*NoneType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyImplicit}
}
func (*AllocatorType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyImplicit}
}
func (*NamedType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyExplicit}
}
func (*TypeParameterType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyExplicit}
}
func (*RawPtrType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyImplicit}
}
func (*FuncType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyExplicit}
}

func (t *DefinedType) ownership(q *ownershipQuery, enumPayload bool) OwnershipCapability {
	return q.check(t.Underlying, enumPayload)
}

// An owned pointer owns its allocation as one value. Its pointee remains a
// structural child, but destroying the pointer is already the ownership act.
func (*OwnedPtrType) ownership(*ownershipQuery, bool) OwnershipCapability {
	return OwnershipCapability{Copy: CopyNever, NeedsDrop: true}
}

func (t *RefType) ownership(*ownershipQuery, bool) OwnershipCapability {
	if t.IsMutable {
		return OwnershipCapability{Copy: CopyNever}
	}
	return OwnershipCapability{Copy: CopyImplicit}
}

func (t *OptionalType) ownership(q *ownershipQuery, _ bool) OwnershipCapability {
	// Optional payloads are ordinary value storage. Being nested in an enum
	// does not turn bulk payload storage into implicit-copy data.
	return q.check(t.Inner, false)
}

func (t *ArrayType) ownership(q *ownershipQuery, _ bool) OwnershipCapability {
	if t.Shape == ArrayOwner {
		return OwnershipCapability{Copy: CopyNever, NeedsDrop: true}
	}
	// Fixed and slice arrays remain bulk storage, but never-copy and drop
	// obligations still propagate from their element.
	return mergeOwnership(OwnershipCapability{Copy: CopyExplicit}, q.check(t.Elem, false))
}

func (t *StructType) ownership(q *ownershipQuery, enumPayload bool) OwnershipCapability {
	result := OwnershipCapability{Copy: CopyExplicit}
	if enumPayload {
		result.Copy = CopyImplicit
	}
	ForEachChild(t, func(child TypeChild) bool {
		result = mergeOwnership(result, q.check(child.Type, false))
		return true
	})
	return result
}

func (*InterfaceType) ownership(*ownershipQuery, bool) OwnershipCapability {
	// Owned-interface drop activation is tracked separately by ownership.
	return OwnershipCapability{Copy: CopyNever}
}

func (t *EnumType) ownership(q *ownershipQuery, _ bool) OwnershipCapability {
	result := OwnershipCapability{Copy: CopyImplicit}
	ForEachChild(t, func(child TypeChild) bool {
		result = mergeOwnership(result, q.check(child.Type, true))
		return true
	})
	return result
}
