package typeinfo

// ownershipCapability answers copy class and drop obligation in one traversal.
//
// IsImplicitCopyType, noCopyType and NeedsDrop each walk the same type
// structure with their own cycle guard, so a type is recursed three times to
// answer three questions about it. They also drift: a case added to one is easy
// to forget in the others.
//
// The three recursions differ in shape, and this keeps both shapes rather than
// flattening them. Implicit copy is a "for all" question — every field of an
// enum payload must itself copy implicitly. No-copy and drop are "there exists"
// questions — one owned field is enough. The enumPayload flag carries the one
// piece of context implicit copy needs: a struct copies implicitly only as an
// enum payload, never as top-level bulk storage.
//
// A cycle answers "not implicitly copyable, no drop", which is what all three
// predicates return when their guard fires.
func ownershipCapability(t Type) OwnershipCapability {
	visiting := make(map[*DefinedType]bool)

	var walk func(Type, bool) capabilityFacts
	walk = func(current Type, enumPayload bool) capabilityFacts {
		if defined, ok := current.(*DefinedType); ok {
			if defined == nil || visiting[defined] {
				return capabilityFacts{}
			}
			visiting[defined] = true
			defer delete(visiting, defined)
			return walk(defined.Underlying, enumPayload)
		}
		switch typ := Underlying(current).(type) {
		case *IntegerType, *ByteType, *CharType, *FloatType, *BoolType,
			*CStrType, *RawPtrType, *AllocatorType, *NoneType:
			return capabilityFacts{implicitCopy: true}
		case *OwnedPtrType, *StringType:
			return capabilityFacts{noCopy: true, drop: true}
		case *InterfaceType:
			// An interface value never copies implicitly, but owned-interface
			// drop activation is tracked separately and is not a drop here.
			return capabilityFacts{noCopy: true}
		case *RefType:
			if typ == nil {
				return capabilityFacts{}
			}
			return capabilityFacts{implicitCopy: !typ.Mutable, noCopy: typ.Mutable}
		case *OptionalType:
			if typ == nil {
				return capabilityFacts{}
			}
			return walk(typ.Inner, false)
		case *ArrayType:
			if typ == nil {
				return capabilityFacts{}
			}
			// An owner array owns its storage whatever the element is.
			if typ.Shape == ArrayOwner {
				return capabilityFacts{noCopy: true, drop: true}
			}
			inner := walk(typ.Elem, false)
			return capabilityFacts{noCopy: inner.noCopy, drop: inner.drop}
		case *StructType:
			if typ == nil {
				return capabilityFacts{}
			}
			// Bulk storage never copies implicitly at top level; as an enum
			// payload it does when every field does.
			facts := capabilityFacts{implicitCopy: enumPayload}
			for _, field := range typ.Fields {
				inner := walk(field.Type, false)
				facts.implicitCopy = facts.implicitCopy && inner.implicitCopy
				facts.noCopy = facts.noCopy || inner.noCopy
				facts.drop = facts.drop || inner.drop
			}
			return facts
		case *EnumType:
			if typ == nil {
				return capabilityFacts{}
			}
			facts := capabilityFacts{implicitCopy: true}
			for _, variant := range typ.Cases {
				if variant.Payload == nil {
					continue
				}
				inner := walk(variant.Payload, true)
				facts.implicitCopy = facts.implicitCopy && inner.implicitCopy
				facts.noCopy = facts.noCopy || inner.noCopy
				facts.drop = facts.drop || inner.drop
			}
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

// capabilityFacts is what one traversal step establishes about a type. It is
// internal to the walk: callers consume OwnershipCapability.
type capabilityFacts struct {
	implicitCopy bool
	noCopy       bool
	drop         bool
}
