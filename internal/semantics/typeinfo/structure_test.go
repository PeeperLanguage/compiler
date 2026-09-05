package typeinfo

import (
	"reflect"
	"testing"
)

func TestForEachChildOwnsCompositeTypeStructure(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	text := &StringType{}
	receiver := &RefType{Target: i32}
	tests := []struct {
		name string
		typ  Type
		want []TypeChild
	}{
		{
			name: "defined",
			typ:  &DefinedType{Underlying: i32},
			want: []TypeChild{{Type: i32, Relation: TypeChildUnderlying}},
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
