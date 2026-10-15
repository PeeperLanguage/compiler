package typelower

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/semantics/types"
)

func TestTypeLowersTheSameSemanticTypeToOneID(t *testing.T) {
	typeTable := ir.NewTypeTable()
	bag := diagnostics.NewDiagnosticBag()
	i32 := &types.IntegerType{IsSigned: true, Bits: 32}

	first := Type(typeTable, bag, i32)
	second := Type(typeTable, bag, i32)
	other := Type(typeTable, bag, &types.IntegerType{IsSigned: true, Bits: 32})
	if first == ir.InvalidType || second != first || other != first {
		t.Fatalf("ids = %d, %d, %d; want one valid id for the same type", first, second, other)
	}
}

// A type that cannot lower is not kept, so each use reports it.
func TestTypeReportsAnInvalidTypeOnEveryUse(t *testing.T) {
	typeTable := ir.NewTypeTable()
	bag := diagnostics.NewDiagnosticBag()
	unresolved := &types.NamedType{Name: "Missing"}

	if id := Type(typeTable, bag, unresolved); id != ir.InvalidType {
		t.Fatalf("unresolved type lowered to %d, want the invalid type", id)
	}
	first := bag.ErrorCount()
	Type(typeTable, bag, unresolved)
	if first == 0 || bag.ErrorCount() != 2*first {
		t.Fatalf("errors after one use = %d, after two = %d; want the error reported again", first, bag.ErrorCount())
	}
}

// A composite whose element failed still gets an ID and is kept, so the
// element's error is reported once, not once per use. This pins that choice.
func TestTypeReportsAFailedElementOnce(t *testing.T) {
	typeTable := ir.NewTypeTable()
	bag := diagnostics.NewDiagnosticBag()
	slice := &types.ArrayType{Shape: types.ArraySlice, Elem: &types.NamedType{Name: "Missing"}}

	first := Type(typeTable, bag, slice)
	reported := bag.ErrorCount()
	second := Type(typeTable, bag, slice)
	if reported == 0 || bag.ErrorCount() != reported || second != first {
		t.Fatalf("errors %d then %d, ids %d then %d; want one report and one id", reported, bag.ErrorCount(), first, second)
	}
}
