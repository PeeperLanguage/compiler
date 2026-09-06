package typeinfo

import (
	"strings"
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

// capabilityGolden records the answer for every type in capabilityMatrix, in
// matrix order, as a copy-class letter (i implicit, e explicit, n never)
// followed by a drop marker (+ or -).
//
// It was captured from the traversal at the point a differential test proved it
// agreed with IsImplicitCopyType, noCopyType and NeedsDrop on every entry.
// Those predicates are gone, so this table is what preserves their coverage. It
// records decided language behavior, not whatever the code happens to do now: a
// diff here means an ownership rule changed and wants a deliberate decision.
const capabilityGolden = "i-i-i-i-i-i-i-i-i-n+n-n+i-n-e-e-e-i-n+e-e-e-i-i-i-n+e-e-e-i-i-i-n+e-e-e-" +
	"i-i-i-n+e-e-e-i-i-i-n+e-e-e-i-i-i-n+e-e-e-i-i-i-n+e-e-e-i-i-i-n+e-e-e-i-" +
	"i-i-n+e-e-e-i-i-n+n+n+n+n+n+n+n-n+n-n-n-n-n-n+n+n+n+n+n+n+i-n+e-e-e-i-i-" +
	"n-n+n-n-n-n-n-e-n+e-e-e-e-e-e-n+e-e-e-e-e-e-n+e-e-e-e-e-i-n+e-e-e-i-" +
	"i-n+n+n+n+n+n+n+e-n+e-e-e-e-e-e-n+e-e-e-i-e-e-n+e-e-e-i-e-i-n+e-e-e-i-i-" +
	"i-n+e-e-e-i-i-" +
	"n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+" +
	"n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+" +
	"n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+n+"

func TestOwnershipCapabilityMatchesGolden(t *testing.T) {
	matrix := capabilityMatrix()
	if len(matrix)*2 != len(capabilityGolden) {
		t.Fatalf("matrix has %d types but golden covers %d; regenerate deliberately",
			len(matrix), len(capabilityGolden)/2)
	}
	var b strings.Builder
	for _, typ := range matrix {
		got := ownershipCapability(typ)
		switch got.Copy {
		case CopyImplicit:
			b.WriteByte('i')
		case CopyExplicit:
			b.WriteByte('e')
		case CopyNever:
			b.WriteByte('n')
		}
		if got.Drop {
			b.WriteByte('+')
		} else {
			b.WriteByte('-')
		}
	}
	got := b.String()
	if got == capabilityGolden {
		return
	}
	for i, typ := range matrix {
		want := capabilityGolden[i*2 : i*2+2]
		if got[i*2:i*2+2] != want {
			t.Errorf("type %d (%s): capability = %s, golden = %s",
				i, TypeText(typ), got[i*2:i*2+2], want)
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

	// Bulk storage never copies implicitly, and the guard firing on the second
	// visit is what makes the self-reference terminate without a drop claim.
	want := OwnershipCapability{Copy: CopyExplicit}
	if got := ownershipCapability(node); got != want {
		t.Fatalf("recursive type: capability = %+v, want %+v", got, want)
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
