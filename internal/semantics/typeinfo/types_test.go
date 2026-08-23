package typeinfo

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/target"
	"slices"
	"testing"
)

func TestTypeFromSyntaxUsesExplicitTargetForSizeIntegers(t *testing.T) {
	target32, err := target.New("linux", "386")
	if err != nil {
		t.Fatal(err)
	}
	target64, err := target.New("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		target target.Info
		bits   int
	}{
		{target: target32, bits: 32},
		{target: target64, bits: 64},
	} {
		typ, ok := TypeFromSyntax(&ast.NamedType{Name: "usize"}, SyntaxOptions{Target: tt.target}).(*IntegerType)
		if !ok || typ.Signed || typ.Bits != tt.bits {
			t.Fatalf("usize type = %#v, want u%d", typ, tt.bits)
		}
	}
}

func TestPointerTypeTextAndEquality(t *testing.T) {
	ownedA := &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	ownedB := &OwnedPtrType{Target: &IntegerType{Signed: true, Bits: 32}}
	rawPtr := &RawPtrType{}
	ref := &RefType{Target: &ArrayType{Shape: ArraySlice, Elem: &StringType{}}}
	opt := &OptionalType{Inner: &IntegerType{Signed: true, Bits: 32}}
	array := &ArrayType{Len: "4", Elem: &IntegerType{Signed: true, Bits: 32}}
	dynArray := &ArrayType{Shape: ArrayOwner, Elem: &StringType{}}

	if got := ownedA.Text(); got != "*i32" {
		t.Fatalf("owned pointer text: got %q want %q", got, "*i32")
	}
	if got := rawPtr.Text(); got != "rawptr" {
		t.Fatalf("raw pointer text: got %q want %q", got, "rawptr")
	}
	if got := ref.Text(); got != "&[..]str" {
		t.Fatalf("reference text: got %q want %q", got, "&[..]str")
	}
	if got := opt.Text(); got != "?i32" {
		t.Fatalf("optional text: got %q want %q", got, "?i32")
	}
	if got := array.Text(); got != "[4]i32" {
		t.Fatalf("array text: got %q want %q", got, "[4]i32")
	}
	if got := dynArray.Text(); got != "[]str" {
		t.Fatalf("dynamic array text: got %q want %q", got, "[]str")
	}
	if !SameType(ownedA, ownedB) {
		t.Fatalf("owned pointers with equal targets should match")
	}
}

func TestSliceIsUnsizedButSliceReferenceIsSized(t *testing.T) {
	slice := &ArrayType{Shape: ArraySlice, Elem: &IntegerType{Signed: true, Bits: 32}}
	if IsSizedType(slice) {
		t.Fatal("bare slice must be unsized")
	}
	if !IsSizedType(&RefType{Target: slice}) || !IsLowerableType(&RefType{Target: slice}) {
		t.Fatal("slice reference must be sized and lowerable")
	}
}

func TestCopyCapabilitiesFollowStructuralModel(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	if !IsImplicitCopyType(i32) || !IsImplicitCopyType(&RawPtrType{}) || !IsImplicitCopyType(&RefType{Target: i32}) {
		t.Fatalf("scalar, raw pointer, and shared reference should copy implicitly")
	}
	if !IsImplicitCopyType(&OptionalType{Inner: i32}) || IsImplicitCopyType(&OptionalType{Inner: &StructType{}}) {
		t.Fatalf("optional copyability should follow payload copyability")
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
	if !IsNoCopyType(&ArrayType{Shape: ArrayOwner, Elem: i32}) {
		t.Fatalf("dynamic array should be intrinsically nocopy")
	}
}

func TestAllocatorCapabilities(t *testing.T) {
	allocator := &AllocatorType{}
	if !IsSizedType(allocator) {
		t.Fatal("allocator must be sized")
	}
	if !IsImplicitCopyType(allocator) {
		t.Fatal("allocator must copy implicitly")
	}
	if !IsEquatable(allocator) {
		t.Fatal("allocator must be equatable")
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
	parameter := &TypeParameterType{Name: "T", OwnerIdentity: "Box", Index: 0}
	if !IsSizedType(parameter) || IsLowerableType(parameter) {
		t.Fatal("declared type parameter must be sized but not backend-lowerable before substitution")
	}
	if IsSizedType(&ArrayType{Shape: ArrayOwner, Elem: iface}) {
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
		Params: []ast.Param{{Type: &ast.NamedType{Name: "Buffer"}}},
	}, SyntaxOptions{}).(*FuncType)
	if got := fn.Text(); got != "fn(Buffer)" {
		t.Fatalf("func text: got %q want %q", got, "fn(Buffer)")
	}
}

func TestTypeFromSyntaxPreservesReferenceReturnContract(t *testing.T) {
	fn := TypeFromSyntax(&ast.FuncType{
		Params:        []ast.Param{{Name: &ast.Ident{Name: "value"}, Type: &ast.RefType{Target: &ast.NamedType{Name: "i32"}}}},
		Return:        &ast.RefType{Target: &ast.NamedType{Name: "i32"}},
		ReturnOrigins: &ast.ReturnOriginClause{Sources: []*ast.Ident{{Name: "value"}}},
	}, SyntaxOptions{}).(*FuncType)
	if fn.ReturnOrigins == nil || !slices.Equal(fn.ReturnOrigins.Sources, []int{0}) {
		t.Fatalf("return origins: %#v", fn.ReturnOrigins)
	}
	if got := fn.Text(); got != "fn(&i32) -> &i32 from value" {
		t.Fatalf("function text: got %q", got)
	}
}

func TestReturnOriginSourcesMapDirectAndMethodSlots(t *testing.T) {
	first := &ast.Ident{Name: "first"}
	second := &ast.Ident{Name: "second"}
	direct := &ast.CallExpr{Callee: &ast.Ident{Name: "choose"}, Args: []ast.Expr{first, second}}
	fn := &FuncType{ReturnOrigins: &ReturnOriginContract{Sources: []int{1, 0, -1, 2}}}
	if got := ReturnOriginSources(direct, fn); !slices.Equal(got, []ast.Expr{second, first}) {
		t.Fatalf("direct return sources = %#v", got)
	}

	receiver := &ast.Ident{Name: "receiver"}
	method := &ast.CallExpr{
		Callee: &ast.SelectorExpr{Expr: receiver, Name: &ast.Ident{Name: "choose"}},
		Args:   []ast.Expr{first, second},
	}
	fn.ReturnOrigins.Sources = []int{0, 2, 3, -1}
	if got := ReturnOriginSources(method, fn); !slices.Equal(got, []ast.Expr{receiver, second}) {
		t.Fatalf("method return sources = %#v", got)
	}
}

func TestTypeFromSyntaxAllowsAbstractSelf(t *testing.T) {
	fn := TypeFromSyntax(&ast.FuncType{
		Params: []ast.Param{{Type: &ast.NamedType{Name: "Self"}}},
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

func TestTypeFromSyntaxRequiresArrayLengthToFitTargetIndex(t *testing.T) {
	target32, err := target.New("linux", "386")
	if err != nil {
		t.Fatal(err)
	}
	target64, err := target.New("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	arrayType := func(length string, compilerTarget target.Info) Type {
		return TypeFromSyntax(&ast.ArrayType{
			Len:  &ast.NumberLit{Value: length, ExplicitType: "u64"},
			Elem: &ast.NamedType{Name: "u8"},
		}, SyntaxOptions{Target: compilerTarget})
	}
	if got := TypeText(arrayType("4294967295", target32)); got != "[4294967295]u8" {
		t.Fatalf("32-bit maximum array type = %s", got)
	}
	if got := arrayType("4294967296", target32); !IsInvalidOrUnknown(got) {
		t.Fatalf("32-bit overflowing array type = %s, want invalid", TypeText(got))
	}
	if got := TypeText(arrayType("4294967296", target64)); got != "[4294967296]u8" {
		t.Fatalf("64-bit array type = %s", got)
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
		{name: "dynamic array", typ: &ArrayType{Shape: ArrayOwner, Elem: &IntegerType{Signed: true, Bits: 32}}, want: true},
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
	nested := &ArrayType{Shape: ArrayOwner, Elem: &ArrayType{Len: "2", Elem: iface}}
	if IsSizedType(nested) {
		t.Fatalf("dynamic array of fixed arrays containing bare interfaces must be unsized")
	}
}

func TestVariantDescriptorUnifiesOptionalAndNamedEnumCases(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	optional, ok := VariantDescriptorOf(&OptionalType{Inner: i32})
	if !ok || optional.Family != VariantFamilyOptional || optional.Identity != "" || len(optional.Cases) != 2 ||
		optional.Cases[0].Name != "Absent" || optional.Cases[0].Payload != nil ||
		optional.Cases[1].Name != "Present" || TypeText(optional.Cases[1].Payload) != "i32" {
		t.Fatalf("optional descriptor = %#v", optional)
	}

	named, ok := VariantDescriptorOf(&DefinedType{
		Name:       "Status",
		Underlying: &EnumType{Cases: []VariantCase{{Name: "Ready"}, {Name: "Waiting"}}},
	})
	if !ok || named.Family != VariantFamilyNamed || named.Identity != "Status" || len(named.Cases) != 2 ||
		named.Cases[0].Name != "Ready" || named.Cases[1].Name != "Waiting" {
		t.Fatalf("named descriptor = %#v", named)
	}
}

func TestNamedEnumPayloadCapabilitiesFollowEveryCaseField(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	owner := &OwnedPtrType{Target: i32}
	copyable := &EnumType{Cases: []VariantCase{
		{Name: "Ready", Payload: &StructType{Fields: []Field{{Name: "value", Type: i32}}}},
		{Name: "Pending"},
	}}
	owned := &EnumType{Cases: []VariantCase{
		{Name: "Ready", Payload: &StructType{Fields: []Field{{Name: "value", Type: owner}}}},
		{Name: "Pending"},
	}}
	unsized := &EnumType{Cases: []VariantCase{{
		Name: "Ready",
		Payload: &StructType{Fields: []Field{{
			Name: "reader",
			Type: &InterfaceType{Methods: []Method{{Name: "read"}}},
		}}},
	}}}
	reference := &EnumType{Cases: []VariantCase{{
		Name: "Borrowed",
		Payload: &StructType{Fields: []Field{{
			Name: "value",
			Type: &RefType{Target: i32},
		}}},
	}}}

	if !IsImplicitCopyType(copyable) || IsNoCopyType(copyable) || NeedsDrop(copyable) {
		t.Fatal("scalar enum payload should remain copyable and require no drop")
	}
	if IsImplicitCopyType(owned) || !IsNoCopyType(owned) || !NeedsDrop(owned) {
		t.Fatal("owned enum payload should be move-only and require drop")
	}
	if IsSizedType(unsized) || IsLowerableType(unsized) {
		t.Fatal("enum payload capabilities must reject unsized cases")
	}
	if !ContainsReference(reference) || !ContainsStoredReference(reference) {
		t.Fatal("enum payload traversal must find stored references")
	}
}

func TestNamedEnumCompatibilityUsesDeclarationAndArguments(t *testing.T) {
	cases := []VariantCase{{Name: "Ready"}, {Name: "Waiting"}}
	left := &DefinedType{
		Name: "Status", Identity: "left::Status", Kind: DefinedKindEnum,
		Underlying: &EnumType{Cases: cases},
	}
	right := &DefinedType{
		Name: "Status", Identity: "right::Status", Kind: DefinedKindEnum,
		Underlying: &EnumType{Cases: cases},
	}
	leftAgain := &DefinedType{
		Name: "Status", Identity: "left::Status", Kind: DefinedKindEnum,
		Underlying: &EnumType{Cases: cases},
	}
	if SameType(left, right) || Assignable(left, right) {
		t.Fatal("different enum declarations must remain nominally distinct")
	}
	if !SameType(left, leftAgain) || !Assignable(left, leftAgain) {
		t.Fatal("same enum declaration and arguments must be compatible")
	}
}

func TestVariantDescriptorUsesEnumIdentityThroughTransparentAlias(t *testing.T) {
	status := &DefinedType{
		Name: "Status", Identity: "module::Status", Kind: DefinedKindEnum,
		Underlying: &EnumType{Cases: []VariantCase{{Name: "Ready"}, {Name: "Waiting"}}},
	}
	alias := &DefinedType{
		Name: "State", Identity: "module::State", Kind: DefinedKindAlias,
		Underlying: status,
	}
	descriptor, ok := VariantDescriptorOf(alias)
	if !ok || descriptor.Identity != status.Identity || descriptor.Family != VariantFamilyNamed {
		t.Fatalf("aliased enum descriptor = %#v, want identity %q", descriptor, status.Identity)
	}
}
