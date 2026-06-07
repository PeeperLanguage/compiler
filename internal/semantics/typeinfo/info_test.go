package typeinfo

import "testing"

func TestPointerTypeTextAndEquality(t *testing.T) {
	ptrA := &RawPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	ptrB := &RawPtrType{Target: &IntegerType{Signed: true, Bits: 32}}

	if got := ptrA.Text(); got != "^i32" {
		t.Fatalf("pointer text: got %q want %q", got, "^i32")
	}
	if !SameType(ptrA, ptrB) {
		t.Fatalf("pointers with equal targets should match")
	}
}
