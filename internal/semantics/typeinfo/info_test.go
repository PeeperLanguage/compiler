package typeinfo

import (
	"compiler/internal/frontend/ast"
	"testing"
)

func TestPointerTypeTextAndEquality(t *testing.T) {
	ownedA := &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	ownedB := &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	rawPtr := &RawPtrType{}
	ref := &RefType{Target: &ArrayType{Dynamic: true, Elem: &StringType{}}}
	opt := &OptionalType{Inner: &IntegerType{Signed: true, Bits: 32}}
	array := &ArrayType{Len: "4", Elem: &IntegerType{Signed: true, Bits: 32}}
	dynArray := &ArrayType{Dynamic: true, Elem: &StringType{}}

	if got := ownedA.Text(); got != "*i32" {
		t.Fatalf("owned pointer text: got %q want %q", got, "*i32")
	}
	if got := rawPtr.Text(); got != "rawptr" {
		t.Fatalf("raw pointer text: got %q want %q", got, "rawptr")
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

func TestCopyCapabilitiesFollowStructuralModel(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	if !IsImplicitCopyType(i32) || !IsImplicitCopyType(&RawPtrType{}) || !IsImplicitCopyType(&RefType{Target: i32}) {
		t.Fatalf("scalar, raw pointer, and shared reference should copy implicitly")
	}
	if IsImplicitCopyType(&StructType{Fields: []Field{{Name: "value", Type: i32}}}) {
		t.Fatalf("struct should not copy implicitly")
	}
	if IsNoCopyType(&StructType{Fields: []Field{{Name: "value", Type: i32}}}) {
		t.Fatalf("scalar-only struct should support structural copy")
	}
	if !IsNoCopyType(&StructType{Fields: []Field{{Name: "owner", Type: &OwnedPtrType{Target: i32}}}}) {
		t.Fatalf("owned pointer should propagate nocopy through struct")
	}
	if !IsNoCopyType(&ArrayType{Dynamic: true, Elem: i32}) {
		t.Fatalf("dynamic array should be intrinsically nocopy")
	}
}

func TestReferenceTargetPreservesMutability(t *testing.T) {
	target := &IntegerType{Signed: true, Bits: 32}
	got, mutable, ok := ReferenceTarget(&RefType{Mutable: true, Target: target})
	if !ok || got != target || !mutable {
		t.Fatalf("reference target = (%v, %v, %v), want (%v, true, true)", got, mutable, ok, target)
	}
	if _, _, ok := ReferenceTarget(&RawPtrType{}); ok {
		t.Fatalf("raw pointer must not classify as reference")
	}
}

func TestReferenceValueTargetUnwrapsOptionalAliases(t *testing.T) {
	target := &IntegerType{Signed: true, Bits: 32}
	valueType := &DefinedType{
		Name: "MaybeReference",
		Underlying: &OptionalType{Inner: &OptionalType{Inner: &DefinedType{
			Name:       "MutableReference",
			Underlying: &RefType{Mutable: true, Target: target},
		}}},
	}
	got, mutable, ok := ReferenceValueTarget(valueType)
	if !ok || got != target || !mutable {
		t.Fatalf("reference value target = (%v, %v, %v), want (%v, true, true)", got, mutable, ok, target)
	}
	if _, _, ok := ReferenceTarget(Underlying(valueType)); ok {
		t.Fatalf("direct reference lookup must not unwrap optional values")
	}
	if _, _, ok := ReferenceValueTarget(&OptionalType{Inner: &RawPtrType{}}); ok {
		t.Fatalf("optional raw pointer must not classify as reference value")
	}
}

func TestSizedTypesDistinguishInterfaceCarriers(t *testing.T) {
	iface := &InterfaceType{Methods: []Method{{Name: "read"}}}
	if IsSizedType(iface) {
		t.Fatalf("bare interface must be unsized")
	}
	for _, carrier := range []Type{
		&RefType{Target: iface},
		&RefType{Mutable: true, Target: iface},
		&OwnedPtrType{Target: iface},
	} {
		if !IsSizedType(carrier) {
			t.Fatalf("interface carrier %s must be sized", TypeText(carrier))
		}
	}
	if !IsSizedType(&RawPtrType{}) {
		t.Fatalf("raw pointer must be sized")
	}
	if !IsSizedType(&NamedType{Name: "T"}) {
		t.Fatalf("generic type parameter must be sized")
	}
	if IsSizedType(&ArrayType{Dynamic: true, Elem: iface}) {
		t.Fatalf("dynamic array cannot contain unsized interface elements")
	}
}

func TestInterfaceTypeOfRecognizesReferencedInterface(t *testing.T) {
	iface := &InterfaceType{Methods: []Method{{Name: "read"}}}
	for _, typ := range []Type{iface, &RefType{Target: iface}, &RefType{Mutable: true, Target: iface}, &OwnedPtrType{Target: iface}} {
		got, ok := InterfaceTypeOf(typ)
		if !ok || got != iface {
			t.Fatalf("interface type = (%v, %v), want (%v, true)", got, ok, iface)
		}
	}
	if _, ok := InterfaceTypeOf(&RefType{Target: &IntegerType{Signed: true, Bits: 32}}); ok {
		t.Fatalf("reference to concrete type must not classify as interface")
	}
	if _, ok := InterfaceTypeOf(&RawPtrType{}); ok {
		t.Fatalf("raw pointer must not classify as interface fat pointer")
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

func TestFuncTypeTextIncludesParams(t *testing.T) {
	fn := &FuncType{
		Params: []Type{&NamedType{Name: "Buffer"}},
	}
	if got := fn.Text(); got != "fn(Buffer)" {
		t.Fatalf("func text: got %q want %q", got, "fn(Buffer)")
	}
}

func TestTypeFromSyntaxPreservesFuncTypeParams(t *testing.T) {
	fn := TypeFromSyntax(&ast.FuncType{
		Params: []ast.TypeExpr{&ast.NamedType{Name: "Buffer"}},
	}, SyntaxOptions{}).(*FuncType)
	if got := fn.Text(); got != "fn(Buffer)" {
		t.Fatalf("func text: got %q want %q", got, "fn(Buffer)")
	}
}

func TestTypeFromSyntaxAllowsAbstractSelf(t *testing.T) {
	fn := TypeFromSyntax(&ast.FuncType{
		Params: []ast.TypeExpr{&ast.NamedType{Name: "Self"}},
	}, SyntaxOptions{AllowAbstractSelf: true}).(*FuncType)
	if got := fn.Text(); got != "fn(Self)" {
		t.Fatalf("func text: got %q want %q", got, "fn(Self)")
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

func TestTypeFromSyntaxRejectsInvalidArrayLengthType(t *testing.T) {
	invalidCalls := 0
	typ := TypeFromSyntax(&ast.ArrayType{
		Len:  &ast.NumberLit{Value: "3", ExplicitType: "f32"},
		Elem: &ast.NamedType{Name: "i32"},
	}, SyntaxOptions{
		InvalidArrayLen: func(*ast.NumberLit) Type {
			invalidCalls++
			return &InvalidType{}
		},
	})
	if !IsInvalidOrUnknown(typ) || invalidCalls != 1 {
		t.Fatalf("array type = %T, invalid callbacks = %d", typ, invalidCalls)
	}

	valid := TypeFromSyntax(&ast.ArrayType{
		Len:  &ast.NumberLit{Value: "3", ExplicitType: "u8"},
		Elem: &ast.NamedType{Name: "i32"},
	}, SyntaxOptions{})
	if TypeText(valid) != "[3]i32" {
		t.Fatalf("valid array type = %s, want [3]i32", TypeText(valid))
	}

	hexadecimal := TypeFromSyntax(&ast.ArrayType{
		Len:  &ast.NumberLit{Value: "0x2"},
		Elem: &ast.NamedType{Name: "i32"},
	}, SyntaxOptions{})
	if TypeText(hexadecimal) != "[2]i32" {
		t.Fatalf("hexadecimal array type = %s, want [2]i32", TypeText(hexadecimal))
	}
}

func TestNeedsDropSeparatesOwnershipFromMoveOnlyTypes(t *testing.T) {
	owner := &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	cases := []struct {
		name string
		typ  Type
		want bool
	}{
		{name: "owned pointer", typ: owner, want: true},
		{name: "string", typ: &StringType{}, want: true},
		{name: "dynamic array", typ: &ArrayType{Dynamic: true, Elem: &IntegerType{Signed: true, Bits: 32}}, want: true},
		{name: "fixed owner array", typ: &ArrayType{Len: "2", Elem: owner}, want: true},
		{name: "optional owner", typ: &OptionalType{Inner: owner}, want: true},
		{name: "nested owner", typ: &StructType{Fields: []Field{{Name: "value", Type: owner}}}, want: true},
		{name: "plain composite", typ: &StructType{Fields: []Field{{Name: "value", Type: &IntegerType{Signed: true, Bits: 32}}}}, want: false},
		{name: "mutable borrow", typ: &RefType{Mutable: true, Target: &IntegerType{Signed: true, Bits: 32}}, want: false},
		{name: "function", typ: &FuncType{}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsDrop(tc.typ); got != tc.want {
				t.Fatalf("NeedsDrop(%s) = %v, want %v", TypeText(tc.typ), got, tc.want)
			}
		})
	}
}

func TestDynamicArrayRequiresRecursivelySizedElement(t *testing.T) {
	iface := &InterfaceType{Methods: []Method{{Name: "read"}}}
	nested := &ArrayType{Dynamic: true, Elem: &ArrayType{Len: "2", Elem: iface}}
	if IsSizedType(nested) {
		t.Fatalf("dynamic array of fixed arrays containing bare interfaces must be unsized")
	}
}
