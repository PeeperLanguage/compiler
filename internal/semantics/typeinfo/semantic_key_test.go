package typeinfo

import "testing"

func TestSemanticKeyUsesCanonicalStructure(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	i64 := &IntegerType{Signed: true, Bits: 64}

	left := &StructType{Fields: []Field{{Name: "value", Type: i32}}}
	rename := &StructType{Fields: []Field{{Name: "code", Type: i32}}}
	widen := &StructType{Fields: []Field{{Name: "value", Type: i64}}}

	base := SemanticKey(left)
	if base == SemanticKey(rename) {
		t.Fatal("field name did not change semantic key")
	}
	if base == SemanticKey(widen) {
		t.Fatal("field type did not change semantic key")
	}
}

func TestSemanticKeyIncludesCallableAndGenericMetadata(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	left := &FuncType{
		Params:        []Type{i32},
		ParamNames:    []string{"left"},
		Return:        i32,
		ReturnOrigins: &ReturnOriginContract{Sources: []int{0}},
	}
	right := &FuncType{
		Params:        []Type{i32},
		ParamNames:    []string{"right"},
		Return:        i32,
		ReturnOrigins: &ReturnOriginContract{Sources: []int{0}},
	}
	withoutOrigin := &FuncType{Params: []Type{i32}, ParamNames: []string{"left"}, Return: i32}
	if SemanticKey(left) == SemanticKey(right) {
		t.Fatal("parameter name did not change semantic key")
	}
	if SemanticKey(left) == SemanticKey(withoutOrigin) {
		t.Fatal("return-origin contract did not change semantic key")
	}

	parameter := &TypeParameterType{Name: "T", OwnerIdentity: "pkg::Box", Index: 0}
	box32 := &DefinedType{
		Name: "Box", Identity: "pkg::Box", Kind: DefinedKindStruct,
		TypeParameters: []*TypeParameterType{parameter}, TypeArguments: []Type{i32},
		Underlying: &StructType{Fields: []Field{{Name: "value", Type: i32}}},
	}
	box64 := &DefinedType{
		Name: "Box", Identity: "pkg::Box", Kind: DefinedKindStruct,
		TypeParameters: []*TypeParameterType{parameter}, TypeArguments: []Type{&IntegerType{Signed: true, Bits: 64}},
		Underlying: box32.Underlying,
	}
	if SemanticKey(box32) == SemanticKey(box64) {
		t.Fatal("generic argument did not change semantic key")
	}
}

func TestSemanticKeyHandlesRecursiveTypesDeterministically(t *testing.T) {
	makeNode := func() Type {
		node := &DefinedType{Name: "Node", Identity: "pkg::Node", Kind: DefinedKindStruct}
		node.Underlying = &StructType{Fields: []Field{{Name: "next", Type: &RefType{Target: node}}}}
		return node
	}
	first := SemanticKey(makeNode())
	second := SemanticKey(makeNode())
	if first == "" || first != second {
		t.Fatalf("recursive semantic keys unstable: %q, %q", first, second)
	}
}

func TestSemanticKeyPreservesAbsentChildSlots(t *testing.T) {
	var typedNil Type = (*IntegerType)(nil)
	nilPayload := &OptionalType{}
	typedNilPayload := &OptionalType{Inner: typedNil}
	if SemanticKey(nilPayload) != SemanticKey(typedNilPayload) {
		t.Fatal("nil and typed-nil payload slots have different semantic keys")
	}
	if SemanticKey(nilPayload) == SemanticKey(&OptionalType{Inner: &IntegerType{Signed: true, Bits: 32}}) {
		t.Fatal("absent and present payload slots have same semantic key")
	}
}

func TestGenericMetadataDoesNotChangeSizedStorageTraversal(t *testing.T) {
	defined := &DefinedType{
		TypeParameters: []*TypeParameterType{{Name: "T", OwnerIdentity: "Box", Index: 0}},
		TypeArguments:  []Type{&UnknownType{}},
		Underlying:     &IntegerType{Signed: true, Bits: 32},
	}
	if !IsSizedType(defined) {
		t.Fatal("generic metadata should not replace the underlying storage edge")
	}
}

func TestSemanticKeyIncludesEnumSchemaAndNominalIdentity(t *testing.T) {
	i32 := &IntegerType{Signed: true, Bits: 32}
	i64 := &IntegerType{Signed: true, Bits: 64}
	makeEnum := func(fieldName string, fieldType Type) *EnumType {
		return &EnumType{Cases: []VariantCase{
			{Name: "Ready", Payload: &StructType{Fields: []Field{{Name: fieldName, Type: fieldType}}}},
			{Name: "Pending"},
		}}
	}
	base := SemanticKey(makeEnum("value", i32))
	if base == SemanticKey(makeEnum("code", i32)) {
		t.Fatal("enum payload field name did not change semantic key")
	}
	if base == SemanticKey(makeEnum("value", i64)) {
		t.Fatal("enum payload field type did not change semantic key")
	}
	if base == SemanticKey(&EnumType{Cases: []VariantCase{{Name: "Waiting"}}}) {
		t.Fatal("enum case name did not change semantic key")
	}

	schema := &EnumType{Cases: []VariantCase{{Name: "Ready"}, {Name: "Waiting"}}}
	left := &DefinedType{Name: "Status", Identity: "left::Status", Kind: DefinedKindEnum, Underlying: schema}
	right := &DefinedType{Name: "Status", Identity: "right::Status", Kind: DefinedKindEnum, Underlying: schema}
	if SemanticKey(left) == SemanticKey(right) {
		t.Fatal("named enum identity did not change semantic key")
	}
}
