package typelower

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/semantics/typeinfo"
)

func TestTypeLowersTheSameSemanticTypeToOneID(t *testing.T) {
	types := ir.NewTypeTable()
	bag := diagnostics.NewDiagnosticBag()
	i32 := &typeinfo.IntegerType{IsSigned: true, Bits: 32}

	first := Type(types, bag, i32)
	second := Type(types, bag, i32)
	other := Type(types, bag, &typeinfo.IntegerType{IsSigned: true, Bits: 32})
	if first == ir.InvalidType || second != first || other != first {
		t.Fatalf("ids = %d, %d, %d; want one valid id for the same type", first, second, other)
	}
}

// A type that cannot lower is not kept, so each use reports it.
func TestTypeReportsAnInvalidTypeOnEveryUse(t *testing.T) {
	types := ir.NewTypeTable()
	bag := diagnostics.NewDiagnosticBag()
	unresolved := &typeinfo.NamedType{Name: "Missing"}

	if id := Type(types, bag, unresolved); id != ir.InvalidType {
		t.Fatalf("unresolved type lowered to %d, want the invalid type", id)
	}
	first := bag.ErrorCount()
	Type(types, bag, unresolved)
	if first == 0 || bag.ErrorCount() != 2*first {
		t.Fatalf("errors after one use = %d, after two = %d; want the error reported again", first, bag.ErrorCount())
	}
}

// A composite whose element failed still gets an ID and is kept, so the
// element's error is reported once, not once per use. This pins that choice.
func TestTypeReportsAFailedElementOnce(t *testing.T) {
	types := ir.NewTypeTable()
	bag := diagnostics.NewDiagnosticBag()
	slice := &typeinfo.ArrayType{Shape: typeinfo.ArraySlice, Elem: &typeinfo.NamedType{Name: "Missing"}}

	first := Type(types, bag, slice)
	reported := bag.ErrorCount()
	second := Type(types, bag, slice)
	if reported == 0 || bag.ErrorCount() != reported || second != first {
		t.Fatalf("errors %d then %d, ids %d then %d; want one report and one id", reported, bag.ErrorCount(), first, second)
	}
}
