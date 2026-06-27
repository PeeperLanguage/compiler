package typeinfo

import "testing"

func TestPointerTypeTextAndEquality(t *testing.T) {
	ptrA := &RawPtrType{Mutable: true, Target: &IntegerType{Signed: true, Bits: 32}}
	ptrB := &RawPtrType{Mutable: true, Target: &IntegerType{Signed: true, Bits: 32}}
	constPtr := &RawPtrType{Mutable: false, Target: &NamedType{Name: "Foo"}}
	opt := &OptionalType{Inner: &IntegerType{Signed: true, Bits: 32}}
	array := &ArrayType{Len: "4", Elem: &IntegerType{Signed: true, Bits: 32}}
	slice := &SliceType{Elem: &StringType{}}

	if got := ptrA.Text(); got != "^i32" {
		t.Fatalf("pointer text: got %q want %q", got, "^i32")
	}
	if got := constPtr.Text(); got != "^const Foo" {
		t.Fatalf("const pointer text: got %q want %q", got, "^const Foo")
	}
	if got := opt.Text(); got != "?i32" {
		t.Fatalf("optional text: got %q want %q", got, "?i32")
	}
	if got := array.Text(); got != "[4]i32" {
		t.Fatalf("array text: got %q want %q", got, "[4]i32")
	}
	if got := slice.Text(); got != "[]string" {
		t.Fatalf("slice text: got %q want %q", got, "[]string")
	}
	if !SameType(ptrA, ptrB) {
		t.Fatalf("pointers with equal targets should match")
	}
}

func TestIsCopyTypeRespectsPointerOwnershipModel(t *testing.T) {
	if IsCopyType(&RawPtrType{Mutable: true, Target: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatalf("^T should be non-copy by default")
	}
	if !IsCopyType(&RawPtrType{Mutable: false, Target: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatalf("^const T should stay copyable")
	}
	if IsCopyType(&SliceType{Elem: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatalf("[]T should be non-copy by default")
	}
	if !IsCopyType(&DefinedType{
		Name:       "Cursor",
		Underlying: &StructType{Fields: []Field{{Name: "ptr", Type: &RawPtrType{Mutable: true, Target: &IntegerType{Signed: true, Bits: 32}}}}},
		CopyMode:   CopyAllow,
	}) {
		t.Fatalf("allow_copy should override default no-copy")
	}
}

func TestFuncTypeTextIncludesMoveParams(t *testing.T) {
	fn := &FuncType{
		Params:   []Type{&NamedType{Name: "Buffer"}},
		Consumes: []bool{true},
	}
	if got := fn.Text(); got != "fn(move Buffer)" {
		t.Fatalf("func text: got %q want %q", got, "fn(move Buffer)")
	}
}
