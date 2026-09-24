package typecheckresult

import (
	"testing"

	"compiler/internal/semantics/typeinfo"
)

func TestCloneReusableExpressionEvidenceCopiesOnlyStableFacts(t *testing.T) {
	src := New()
	dst := New()
	const srcID = 10
	const dstID = 20

	src.RecordExprType(srcID, &typeinfo.IntegerType{IsSigned: true, Bits: 32})
	src.MarkExpandedDefaultBinding(srcID)
	src.RecordInterfaceImplementations(srcID, []InterfaceImplementation{{}})
	src.RecordImplicitConversion(srcID, typeinfo.Conversion{Kind: typeinfo.ConversionNumeric, Compatibility: typeinfo.Compatible})
	src.RecordStructField(srcID, StructFieldAccess{Field: 2, Type: &typeinfo.IntegerType{IsSigned: true, Bits: 32}})

	// These depend on the cloned expression's new context and must be recomputed.
	src.RecordValueUse(srcID, typeinfo.UseMove)
	src.RecordReferenceArgument(srcID, true)
	src.RecordCaseTest(srcID, CaseTest{SubjectID: 99, Case: 1, CaseCount: 2})

	dst.CloneReusableExpressionEvidenceFrom(dstID, src, srcID)

	if got := typeinfo.TypeText(dst.ExprType(dstID)); got != "i32" {
		t.Fatalf("cloned type = %s, want i32", got)
	}
	if !dst.ExpandedDefaultBinding(dstID) {
		t.Fatal("default-binding provenance was not cloned")
	}
	if got := len(dst.InterfaceImplementations(dstID)); got != 1 {
		t.Fatalf("cloned interface implementations = %d, want 1", got)
	}
	if conversion, ok := dst.ImplicitConversion(dstID); !ok || conversion.Kind != typeinfo.ConversionNumeric {
		t.Fatalf("cloned conversion = %#v, found=%v", conversion, ok)
	}
	if field, ok := dst.StructField(dstID); !ok || field.Field != 2 {
		t.Fatalf("cloned field = %#v, found=%v", field, ok)
	}
	if _, ok := dst.ValueUse(dstID); ok {
		t.Fatal("contextual value use must not be cloned")
	}
	if _, ok := dst.ReferenceArgument(dstID); ok {
		t.Fatal("contextual reference argument must not be cloned")
	}
	if _, ok := dst.CaseTest(dstID); ok {
		t.Fatal("case-test subject identity must not be cloned")
	}
}
