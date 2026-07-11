package typeinfo

import (
	"compiler/internal/frontend/ast"
	"testing"
)

func TestPointerTypeTextAndEquality(t *testing.T) {
	ownedA := &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	ownedB := &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	rawPtr := &RawPtrType{Target: &NamedType{Name: "Foo"}}
	ref := &RefType{Target: &ArrayType{Dynamic: true, Elem: &StringType{}}}
	opt := &OptionalType{Inner: &IntegerType{Signed: true, Bits: 32}}
	array := &ArrayType{Len: "4", Elem: &IntegerType{Signed: true, Bits: 32}}
	dynArray := &ArrayType{Dynamic: true, Elem: &StringType{}}

	if got := ownedA.Text(); got != "^i32" {
		t.Fatalf("owned pointer text: got %q want %q", got, "^i32")
	}
	if got := rawPtr.Text(); got != "*Foo" {
		t.Fatalf("raw pointer text: got %q want %q", got, "*Foo")
	}
	if got := ref.Text(); got != "&[]string" {
		t.Fatalf("reference text: got %q want %q", got, "&[]string")
	}
	if got := opt.Text(); got != "?i32" {
		t.Fatalf("optional text: got %q want %q", got, "?i32")
	}
	if got := array.Text(); got != "[4]i32" {
		t.Fatalf("array text: got %q want %q", got, "[4]i32")
	}
	if got := dynArray.Text(); got != "[]string" {
		t.Fatalf("dynamic array text: got %q want %q", got, "[]string")
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
	if IsCopyType(&ArrayType{Dynamic: true, Elem: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatalf("[]T should be non-copy by default")
	}
	if !IsCopyType(&RefType{Target: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatalf("&T should be copyable")
	}
	if IsCopyType(&RefType{Mutable: true, Target: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatalf("&mut T should be non-copy")
	}
	if !IsCopyType(&DefinedType{
		Name:       "Cursor",
		Underlying: &StructType{Fields: []Field{{Name: "ptr", Type: &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}}}},
		CopyMode:   CopyAllow,
	}) {
		t.Fatalf("allow_copy should override default no-copy")
	}
}

func TestReferenceTargetPreservesMutability(t *testing.T) {
	target := &IntegerType{Signed: true, Bits: 32}
	got, mutable, ok := ReferenceTarget(&RefType{Mutable: true, Target: target})
	if !ok || got != target || !mutable {
		t.Fatalf("reference target = (%v, %v, %v), want (%v, true, true)", got, mutable, ok, target)
	}
	if _, _, ok := ReferenceTarget(&RawPtrType{Target: target}); ok {
		t.Fatalf("raw pointer must not classify as reference")
	}
}

func TestInterfaceTypeOfRecognizesReferencedInterface(t *testing.T) {
	iface := &InterfaceType{Methods: []Method{{Name: "read"}}}
	for _, typ := range []Type{iface, &RefType{Target: iface}, &RefType{Mutable: true, Target: iface}} {
		got, ok := InterfaceTypeOf(typ)
		if !ok || got != iface {
			t.Fatalf("interface type = (%v, %v), want (%v, true)", got, ok, iface)
		}
	}
	if _, ok := InterfaceTypeOf(&RefType{Target: &IntegerType{Signed: true, Bits: 32}}); ok {
		t.Fatalf("reference to concrete type must not classify as interface")
	}
}

func TestContainsReferenceTraversesAliasesAndStopsAtCycles(t *testing.T) {
	referenceAlias := &DefinedType{
		Name:       "Shared",
		Underlying: &RefType{Target: &IntegerType{Signed: true, Bits: 32}},
	}
	if !ContainsReference(&ArrayType{Len: "2", Elem: referenceAlias}) {
		t.Fatalf("reference hidden by alias and array should be found")
	}

	recursive := &DefinedType{Name: "Node"}
	recursive.Underlying = &StructType{Fields: []Field{{
		Name: "next",
		Type: &OwnedPtrType{Target: recursive},
	}}}
	if ContainsReference(recursive) {
		t.Fatalf("reference-free recursive type should not report a reference")
	}
}

func TestReferenceStorageTraversalIgnoresCallableMetadata(t *testing.T) {
	reference := &RefType{Target: &IntegerType{Signed: true, Bits: 32}}
	callback := &FuncType{Params: []Type{reference}, Return: &IntegerType{Signed: true, Bits: 32}}
	holder := &StructType{Fields: []Field{{Name: "callback", Type: callback}}}

	if ContainsReference(callback) || ContainsReference(holder) {
		t.Fatalf("callable signature metadata should not count as stored references")
	}
	if ContainsStoredReference(reference) {
		t.Fatalf("direct reference should remain valid as a temporary local or parameter")
	}
	if ContainsStoredReference(&OptionalType{Inner: reference}) {
		t.Fatalf("optional reference should remain valid outside a storage boundary")
	}
	if !ContainsStoredReference(&ArrayType{Len: "2", Elem: reference}) {
		t.Fatalf("array elements should count as stored references")
	}
	if !ContainsStoredReference(&OwnedPtrType{Target: reference}) {
		t.Fatalf("heap-owned reference target should count as stored")
	}
}

func TestContainsAbstractSelfDoesNotExpandResolvedTypes(t *testing.T) {
	resolved := &DefinedType{
		Name: "Resolved",
		Underlying: &InterfaceType{Methods: []Method{{
			Name: "read",
			Params: []Field{{
				Name: "self",
				Type: &NamedType{Name: "Self"},
			}},
		}}},
	}
	if ContainsAbstractSelf(resolved) {
		t.Fatalf("resolved defined type should not be treated as an abstract Self occurrence")
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
	}, SyntaxOptions{}).(*FuncType)
	if len(fn.Consumes) != 1 || !fn.Consumes[0] {
		t.Fatalf("expected consuming first param, got %#v", fn.Consumes)
	}
	if got := fn.Text(); got != "fn(move Buffer)" {
		t.Fatalf("func text: got %q want %q", got, "fn(move Buffer)")
	}
}

func TestTypeFromSyntaxAllowsAbstractSelf(t *testing.T) {
	fn := TypeFromSyntax(&ast.FuncType{
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

func TestTypeFromSyntaxAppliesResolversRecursively(t *testing.T) {
	resolved := &DefinedType{Name: "Resolved"}
	typ := TypeFromSyntax(&ast.RefType{
		Target: &ast.OptionalType{Inner: &ast.NamedType{Name: "Alias"}},
	}, SyntaxOptions{
		ResolveNamed: func(name string) (Type, bool) {
			return resolved, name == "Alias"
		},
	})

	ref, ok := typ.(*RefType)
	if !ok {
		t.Fatalf("expected reference type, got %T", typ)
	}
	optional, ok := ref.Target.(*OptionalType)
	if !ok {
		t.Fatalf("expected optional target, got %T", ref.Target)
	}
	if optional.Inner != resolved {
		t.Fatalf("expected resolved nested type, got %T", optional.Inner)
	}
}
