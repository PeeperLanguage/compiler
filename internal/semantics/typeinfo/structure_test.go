package typeinfo

import (
	"reflect"
	"testing"
)

func TestForEachChildOwnsCompositeTypeStructure(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	text := &StringType{}
	parameter := &TypeParameterType{Name: "T", OwnerIdentity: "Box", Index: 0}
	receiver := &RefType{Target: i32}
	tests := []struct {
		name string
		typ  Type
		want []TypeChild
	}{
		{
			name: "defined",
			typ: &DefinedType{
				Underlying:     i32,
				TypeParameters: []*TypeParameterType{parameter},
				TypeArguments:  []Type{text},
			},
			want: []TypeChild{
				{Type: i32, Relation: TypeChildUnderlying},
				{Type: parameter, Relation: TypeChildTypeParameter},
				{Type: text, Relation: TypeChildTypeArgument},
			},
		},
		{
			name: "owned target",
			typ:  &OwnedPtrType{Target: text},
			want: []TypeChild{{Type: text, Relation: TypeChildOwnedTarget}},
		},
		{
			name: "borrowed target",
			typ:  &RefType{Target: text},
			want: []TypeChild{{Type: text, Relation: TypeChildBorrowedTarget}},
		},
		{
			name: "optional payload",
			typ:  &OptionalType{Inner: text},
			want: []TypeChild{{Type: text, Relation: TypeChildOptionalPayload}},
		},
		{
			name: "array element",
			typ:  &ArrayType{Len: "4", Elem: text},
			want: []TypeChild{{Type: text, Relation: TypeChildArrayElement}},
		},
		{
			name: "struct fields",
			typ:  &StructType{Fields: []Field{{Name: "left", Type: i32}, {Name: "right", Type: text}}},
			want: []TypeChild{{Type: i32, Relation: TypeChildStructField}, {Type: text, Relation: TypeChildStructField}},
		},
		{
			name: "enum payloads",
			typ:  &EnumType{Cases: []VariantCase{{Name: "Empty"}, {Name: "Value", Payload: text}}},
			want: []TypeChild{{Type: text, Relation: TypeChildEnumPayload}},
		},
		{
			name: "function signature",
			typ:  &FuncType{Params: []Type{i32}, Return: text},
			want: []TypeChild{{Type: i32, Relation: TypeChildCallableParameter}, {Type: text, Relation: TypeChildCallableReturn}},
		},
		{
			name: "interface methods",
			typ: &InterfaceType{Methods: []Method{{
				Name:   "read",
				Params: []Field{{Name: "self", Type: receiver}},
				Return: text,
			}}},
			want: []TypeChild{{Type: receiver, Relation: TypeChildMethodReceiver}, {Type: text, Relation: TypeChildCallableReturn}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []TypeChild
			ForEachChild(test.typ, func(child TypeChild) bool {
				got = append(got, child)
				return true
			})
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ForEachChild(%T) = %#v, want %#v", test.typ, got, test.want)
			}
		})
	}
}

func TestTransformChildrenPreservesTypeMetadataAndSlots(t *testing.T) {
	original := &FuncType{
		Params:        []Type{&IntegerType{Signed: true, Bits: 32}},
		ParamNames:    []string{"value"},
		ReturnOrigins: &ReturnOriginContract{Sources: []int{0}},
	}
	transformed := TransformChildren(original, func(child TypeChild) Type {
		if integer, ok := child.Type.(*IntegerType); ok && integer != nil {
			return &IntegerType{Signed: integer.Signed, Bits: 64}
		}
		return child.Type
	})
	fn, ok := transformed.(*FuncType)
	if !ok || len(fn.Params) != 1 {
		t.Fatalf("transformed type = %#v", transformed)
	}
	if TypeText(fn.Params[0]) != "i64" || fn.ParamNames[0] != "value" {
		t.Fatalf("transformed function metadata = %#v", fn)
	}
	if fn.Return != nil || fn.ReturnOrigins == nil || !reflect.DeepEqual(fn.ReturnOrigins.Sources, []int{0}) {
		t.Fatalf("transformed function slots = %#v", fn)
	}
}

func TestTransformChildrenLeavesNominalTypesAtomic(t *testing.T) {
	defined := &DefinedType{
		Name:       "Node",
		Identity:   "test::Node",
		Underlying: &StructType{Fields: []Field{{Name: "value", Type: &NamedType{Name: "Self"}}}},
	}
	got := TransformChildren(defined, func(child TypeChild) Type {
		t.Fatalf("nominal type yielded child %v", child.Relation)
		return nil
	})
	if got != defined {
		t.Fatalf("nominal transform changed identity: got %p want %p", got, defined)
	}
}

func TestReplaceAbstractSelfUsesCanonicalChildTransform(t *testing.T) {
	resolved := &DefinedType{Name: "Buffer", Identity: "test::Buffer"}
	method := &FuncType{
		Params: []Type{&RefType{Target: &NamedType{Name: "Self"}}},
		Return: &OptionalType{Inner: &NamedType{Name: "Self"}},
	}
	got, ok := ReplaceAbstractSelf(method, resolved).(*FuncType)
	if !ok || TypeText(got.Params[0]) != "&Buffer" || TypeText(got.Return) != "?Buffer" {
		t.Fatalf("replaced function = %#v", got)
	}
}

func TestTypeStructureDrivesRecursiveContainment(t *testing.T) {
	stored := &StructType{Fields: []Field{{Name: "borrow", Type: &RefType{Target: &IntegerType{Signed: true, Bits: 32}}}}}
	wrapped := &OptionalType{Inner: &ArrayType{Len: "2", Elem: stored}}

	if !ContainsReference(wrapped) {
		t.Fatal("nested structural type should contain a reference")
	}
	if !ContainsStoredReference(wrapped) {
		t.Fatal("reference inside stored composite children should be stored")
	}
}

func TestLeafTypeTraversalCompletesWithoutYield(t *testing.T) {
	for _, typ := range []Type{
		&InvalidType{}, &UnknownType{}, &IntegerType{}, &ByteType{}, &CharType{},
		&FloatType{}, &BoolType{}, &CStrType{}, &StringType{}, &NoneType{},
		&AllocatorType{}, &NamedType{}, &TypeParameterType{}, &RawPtrType{},
	} {
		if !ForEachChild(typ, func(TypeChild) bool {
			t.Errorf("leaf %T yielded a child", typ)
			return false
		}) || !ForEachChild(typ, nil) {
			t.Errorf("leaf %T traversal did not complete", typ)
		}
	}
}

func TestNilTypeTraversalAndOwnership(t *testing.T) {
	for _, typ := range []Type{
		nil, (*InvalidType)(nil), (*UnknownType)(nil), (*IntegerType)(nil),
		(*ByteType)(nil), (*CharType)(nil), (*FloatType)(nil), (*BoolType)(nil),
		(*CStrType)(nil), (*StringType)(nil), (*NoneType)(nil), (*AllocatorType)(nil),
		(*NamedType)(nil), (*TypeParameterType)(nil), (*RawPtrType)(nil),
		(*DefinedType)(nil), (*OwnedPtrType)(nil), (*RefType)(nil),
		(*OptionalType)(nil), (*ArrayType)(nil), (*FuncType)(nil),
		(*StructType)(nil), (*InterfaceType)(nil), (*EnumType)(nil),
	} {
		if !ForEachChild(typ, func(TypeChild) bool {
			t.Errorf("nil %T yielded a child", typ)
			return false
		}) {
			t.Errorf("nil %T traversal did not complete", typ)
		}
		if got := ownershipCapability(typ); got != (OwnershipCapability{Copy: CopyExplicit}) {
			t.Errorf("nil %T capability = %+v; want explicit copy, no drop", typ, got)
		}
	}
}

func TestForEachChildAcceptsTypedNilTypes(t *testing.T) {
	var optional *OptionalType
	var typ Type = optional
	called := false
	if !ForEachChild(typ, func(TypeChild) bool {
		called = true
		return true
	}) {
		t.Fatal("typed nil traversal should complete")
	}
	if called {
		t.Fatal("typed nil type should have no children")
	}
}
