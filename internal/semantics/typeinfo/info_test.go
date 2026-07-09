package typeinfo

import (
	"compiler/internal/frontend/ast"
	"testing"
)

func TestPointerTypeTextAndEquality(t *testing.T) {
	ownedA := &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	ownedB := &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	rawPtr := &RawPtrType{Target: &NamedType{Name: "Foo"}}
	opt := &OptionalType{Inner: &IntegerType{Signed: true, Bits: 32}}
	array := &ArrayType{Len: "4", Elem: &IntegerType{Signed: true, Bits: 32}}
	slice := &SliceType{Elem: &StringType{}}

	if got := ownedA.Text(); got != "^i32" {
		t.Fatalf("owned pointer text: got %q want %q", got, "^i32")
	}
	if got := rawPtr.Text(); got != "*Foo" {
		t.Fatalf("raw pointer text: got %q want %q", got, "*Foo")
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
	if !SameType(ownedA, ownedB) {
		t.Fatalf("owned pointers with equal targets should match")
	}
}

func TestIsCopyTypeRespectsPointerOwnershipModel(t *testing.T) {
	if IsCopyType(&OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatalf("^T should be non-copy by default")
	}
	if !IsCopyType(&RawPtrType{Target: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatalf("*T should be copyable")
	}
	if IsCopyType(&SliceType{Elem: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatalf("[]T should be non-copy by default")
	}
	if !IsCopyType(&DefinedType{
		Name:       "Cursor",
		Underlying: &StructType{Fields: []Field{{Name: "ptr", Type: &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}}}},
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

func TestTypeFromSyntaxPreservesFuncTypeConsumes(t *testing.T) {
	fn := TypeFromSyntax(&ast.FuncType{
		Params:   []ast.TypeExpr{&ast.NamedType{Name: "Buffer"}},
		Consumes: []bool{true},
	}).(*FuncType)
	if len(fn.Consumes) != 1 || !fn.Consumes[0] {
		t.Fatalf("expected consuming first param, got %#v", fn.Consumes)
	}
	if got := fn.Text(); got != "fn(move Buffer)" {
		t.Fatalf("func text: got %q want %q", got, "fn(move Buffer)")
	}
}

func TestASTTypeWithOptionsPreservesFuncTypeConsumes(t *testing.T) {
	fn := ASTTypeWithOptions(&ast.FuncType{
		Params:   []ast.TypeExpr{&ast.NamedType{Name: "Self"}},
		Consumes: []bool{true},
	}, SyntaxOptions{AllowAbstractSelf: true}).(*FuncType)
	if len(fn.Consumes) != 1 || !fn.Consumes[0] {
		t.Fatalf("expected consuming first param, got %#v", fn.Consumes)
	}
	if got := fn.Text(); got != "fn(move Self)" {
		t.Fatalf("func text: got %q want %q", got, "fn(move Self)")
	}
}
