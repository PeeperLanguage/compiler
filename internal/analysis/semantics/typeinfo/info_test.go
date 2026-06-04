package typeinfo

import "testing"

func TestReferenceAndRawPointerTypeTextAndEquality(t *testing.T) {
	sharedA := &RefType{Target: &IntegerType{Signed: true, Bits: 32}}
	sharedB := &RefType{Target: &IntegerType{Signed: true, Bits: 32}}
	mutableRef := &RefType{Mutable: true, Target: &IntegerType{Signed: true, Bits: 32}}
	constPtr := &RawPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	mutablePtr := &RawPtrType{Mutable: true, Target: &IntegerType{Signed: true, Bits: 32}}

	if got := sharedA.Text(); got != "&i32" {
		t.Fatalf("shared ref text: got %q want %q", got, "&i32")
	}
	if got := mutableRef.Text(); got != "&mut i32" {
		t.Fatalf("mutable ref text: got %q want %q", got, "&mut i32")
	}
	if got := constPtr.Text(); got != "*const i32" {
		t.Fatalf("const ptr text: got %q want %q", got, "*const i32")
	}
	if got := mutablePtr.Text(); got != "*mut i32" {
		t.Fatalf("mutable ptr text: got %q want %q", got, "*mut i32")
	}
	if !SameType(sharedA, sharedB) {
		t.Fatalf("shared refs with equal targets should match")
	}
	if SameType(sharedA, mutableRef) {
		t.Fatalf("shared and mutable refs should not match")
	}
	if SameType(constPtr, mutablePtr) {
		t.Fatalf("const and mutable raw pointers should not match")
	}
}
