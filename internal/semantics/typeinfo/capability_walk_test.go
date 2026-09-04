package typeinfo

import (
	"testing"
)

// leafTypes covers every base case the three ownership predicates distinguish.
func leafTypes() []Type {
	return []Type{
		&IntegerType{}, &ByteType{}, &CharType{}, &FloatType{}, &BoolType{},
		&CStrType{}, &RawPtrType{}, &AllocatorType{}, &NoneType{},
		&StringType{}, &InterfaceType{},
		&OwnedPtrType{Target: &IntegerType{}},
		&RefType{Target: &IntegerType{}},
		&RefType{Target: &IntegerType{}, Mutable: true},
		&TypeParameterType{Name: "T"},
		&InvalidType{},
		&UnknownType{},
	}
}

// wrap produces every one-level composite around a type, so the matrix reaches
// the recursive branches of all three predicates.
func wrap(inner Type) []Type {
	return []Type{
		&OptionalType{Inner: inner},
		&ArrayType{Shape: ArrayOwner, Elem: inner},
		&ArrayType{Shape: ArraySlice, Elem: inner},
		&StructType{Fields: []Field{{Name: "a", Type: inner}}},
		&StructType{Fields: []Field{{Name: "a", Type: &IntegerType{}}, {Name: "b", Type: inner}}},
		&EnumType{Cases: []VariantCase{{Name: "None"}, {Name: "Some", Payload: inner}}},
		&DefinedType{Name: "Named", Underlying: inner},
	}
}

// capabilityMatrix builds leaves, one-level and two-level composites.
func capabilityMatrix() []Type {
	matrix := leafTypes()
	for _, leaf := range leafTypes() {
		matrix = append(matrix, wrap(leaf)...)
	}
	for _, leaf := range []Type{&IntegerType{}, &StringType{}, &OwnedPtrType{Target: &IntegerType{}}} {
		for _, once := range wrap(leaf) {
			matrix = append(matrix, wrap(once)...)
		}
	}
	return matrix
}

// The single traversal must answer exactly what the three separate predicates
// answer today. This is the parity proof that has to pass before any caller is
// migrated or any predicate deleted.
func TestOwnershipCapabilityWalkMatchesEstablishedPredicates(t *testing.T) {
	matrix := capabilityMatrix()
	if len(matrix) < 100 {
		t.Fatalf("matrix has %d types, too few to be meaningful", len(matrix))
	}
	for index, typ := range matrix {
		want := OwnershipCapability{Copy: CopyExplicit, Drop: NeedsDrop(typ)}
		switch {
		case IsImplicitCopyType(typ):
			want.Copy = CopyImplicit
		case noCopyType(typ):
			want.Copy = CopyNever
		}
		got := ownershipCapability(typ)
		if got != want {
			t.Errorf("type %d (%s): walk = %+v, predicates = %+v", index, TypeText(typ), got, want)
		}
	}
}

// A type that contains itself must terminate and answer what the guarded
// predicates answer, rather than recursing forever.
func TestOwnershipCapabilityWalkTerminatesOnRecursiveType(t *testing.T) {
	node := &DefinedType{Name: "Node"}
	node.Underlying = &StructType{Fields: []Field{
		{Name: "next", Type: &OptionalType{Inner: node}},
		{Name: "value", Type: &IntegerType{}},
	}}

	want := OwnershipCapability{Copy: CopyExplicit, Drop: NeedsDrop(node)}
	switch {
	case IsImplicitCopyType(node):
		want.Copy = CopyImplicit
	case noCopyType(node):
		want.Copy = CopyNever
	}
	if got := ownershipCapability(node); got != want {
		t.Fatalf("recursive type: walk = %+v, predicates = %+v", got, want)
	}
}

// A type reached twice down separate branches is not a cycle, so the guard must
// not suppress the second visit.
func TestOwnershipCapabilityWalkVisitsRepeatedTypeTwice(t *testing.T) {
	owned := &DefinedType{Name: "Owned", Underlying: &OwnedPtrType{Target: &IntegerType{}}}
	pair := &StructType{Fields: []Field{{Name: "a", Type: owned}, {Name: "b", Type: owned}}}

	got := ownershipCapability(pair)
	if !got.Drop {
		t.Fatalf("repeated owned field: walk = %+v, want a drop obligation", got)
	}
	if got.Copy != CopyNever {
		t.Fatalf("repeated owned field: walk = %+v, want CopyNever", got)
	}
}
