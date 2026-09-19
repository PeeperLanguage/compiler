package typeinfo

import (
	"strconv"

	"compiler/pkg/typednil"
)

// TypeChildRelation describes why one semantic type contains another. The
// relation is structural evidence, not an analysis result: ownership, sizing,
// lowerability, substitution, and future queries may interpret the same child
// differently while sharing one canonical declaration of where that child is.
type TypeChildRelation uint8

const (
	TypeChildUnderlying TypeChildRelation = iota
	TypeChildOwnedTarget
	TypeChildBorrowedTarget
	TypeChildOptionalPayload
	TypeChildArrayElement
	TypeChildStructField
	TypeChildEnumPayload
	TypeChildMethodReceiver
	TypeChildCallableParameter
	TypeChildCallableReturn
	TypeChildTypeParameter
	TypeChildTypeArgument
)

// TypeChild is one immediate semantic-type edge. Recursive consumers interpret
// relation semantics without rediscovering concrete type fields.
type TypeChild struct {
	Type     Type
	Relation TypeChildRelation
}

// typeStructure is the canonical local structure of one semantic type.
// Semantic identity consumes all fields; structural analyses project only
// present children through ForEachChild.
type typeStructure struct {
	kind       string
	attributes []string
	children   []TypeChild
}

func newTypeStructure(kind string, attributes ...string) typeStructure {
	return typeStructure{kind: kind, attributes: attributes}
}

// ForEachChild visits the immediate semantic children of typ in source/semantic
// order. It returns false when yield asks traversal to stop.
//
// This is the semantic-type equivalent of ast.Node.forEachChild: it owns type
// structure once while leaving recursion policy to the consumer. Cycle rules
// deliberately stay with each analysis because sizedness, lowerability, and
// ownership do not assign the same meaning to recursive edges.
func ForEachChild(typ Type, yield func(TypeChild) bool) bool {
	if typ == nil || typednil.IsNil(typ) || yield == nil {
		return true
	}
	for _, child := range typ.structure().children {
		if child.Type == nil || typednil.IsNil(child.Type) {
			continue
		}
		if !yield(child) {
			return false
		}
	}
	return true
}

// TransformChildren applies one structural rewrite to every immediate child
// slot, including nil slots. It deliberately does not recurse: analyses own
// cycle and nominal-type policies, while this operation preserves one type's
// metadata and shape.
func TransformChildren(typ Type, transform func(TypeChild) Type) Type {
	if typ == nil || typednil.IsNil(typ) || transform == nil {
		return typ
	}
	if _, nominal := typ.(nominalType); nominal {
		return typ
	}
	children := typ.structure().children
	if len(children) == 0 {
		return typ
	}
	transformed := make([]TypeChild, len(children))
	for index, child := range children {
		transformed[index] = child
		transformed[index].Type = transform(child)
	}
	return typ.withChildren(transformed)
}

type nominalType interface {
	Type
	nominal()
}

func (*InvalidType) structure() typeStructure   { return newTypeStructure("invalid") }
func (*UnknownType) structure() typeStructure   { return newTypeStructure("unknown") }
func (*ByteType) structure() typeStructure      { return newTypeStructure("byte") }
func (*CharType) structure() typeStructure      { return newTypeStructure("char") }
func (*BoolType) structure() typeStructure      { return newTypeStructure("bool") }
func (*CStrType) structure() typeStructure      { return newTypeStructure("cstr") }
func (*StringType) structure() typeStructure    { return newTypeStructure("string") }
func (*NoneType) structure() typeStructure      { return newTypeStructure("none") }
func (*AllocatorType) structure() typeStructure { return newTypeStructure("allocator") }
func (*RawPtrType) structure() typeStructure    { return newTypeStructure("rawptr") }

func (t *IntegerType) structure() typeStructure {
	return newTypeStructure("integer", strconv.FormatBool(t.Signed), strconv.Itoa(t.Bits))
}

func (t *FloatType) structure() typeStructure {
	return newTypeStructure("float", strconv.Itoa(t.Bits))
}

func (t *NamedType) structure() typeStructure {
	return newTypeStructure("named", t.Name)
}

func (t *TypeParameterType) structure() typeStructure {
	return newTypeStructure("parameter", t.OwnerIdentity, strconv.Itoa(t.Index), t.Name)
}

func (t *DefinedType) structure() typeStructure {
	structure := newTypeStructure("defined", strconv.Itoa(int(t.Kind)), t.Identity, t.Name)
	structure.children = make([]TypeChild, 0, 1+len(t.TypeParameters)+len(t.TypeArguments))
	structure.children = append(structure.children, TypeChild{Type: t.Underlying, Relation: TypeChildUnderlying})
	for _, parameter := range t.TypeParameters {
		structure.children = append(structure.children, TypeChild{Type: parameter, Relation: TypeChildTypeParameter})
	}
	for _, argument := range t.TypeArguments {
		structure.children = append(structure.children, TypeChild{Type: argument, Relation: TypeChildTypeArgument})
	}
	return structure
}

func (t *OwnedPtrType) structure() typeStructure {
	return typeStructure{kind: "owned", children: []TypeChild{{Type: t.Target, Relation: TypeChildOwnedTarget}}}
}

func (t *RefType) structure() typeStructure {
	return typeStructure{
		kind:       "ref",
		attributes: []string{strconv.FormatBool(t.Mutable)},
		children:   []TypeChild{{Type: t.Target, Relation: TypeChildBorrowedTarget}},
	}
}

func (t *OptionalType) structure() typeStructure {
	return typeStructure{kind: "optional", children: []TypeChild{{Type: t.Inner, Relation: TypeChildOptionalPayload}}}
}

func (t *ArrayType) structure() typeStructure {
	return typeStructure{
		kind:       "array",
		attributes: []string{strconv.Itoa(int(t.Shape)), t.Len},
		children:   []TypeChild{{Type: t.Elem, Relation: TypeChildArrayElement}},
	}
}

func (t *FuncType) structure() typeStructure {
	structure := typeStructure{
		kind:       "func",
		attributes: make([]string, 0, len(t.Params)+2),
		children:   make([]TypeChild, 0, len(t.Params)+1),
	}
	for index, param := range t.Params {
		name := ""
		if index < len(t.ParamNames) {
			name = t.ParamNames[index]
		}
		structure.attributes = append(structure.attributes, name)
		structure.children = append(structure.children, TypeChild{Type: param, Relation: TypeChildCallableParameter})
	}
	structure.attributes = appendOriginAttributes(structure.attributes, t.ReturnOrigins)
	structure.children = append(structure.children, TypeChild{Type: t.Return, Relation: TypeChildCallableReturn})
	return structure
}

func (t *StructType) structure() typeStructure {
	structure := typeStructure{
		kind:       "struct",
		attributes: make([]string, 0, len(t.Fields)),
		children:   make([]TypeChild, 0, len(t.Fields)),
	}
	for _, field := range t.Fields {
		structure.attributes = append(structure.attributes, field.Name)
		structure.children = append(structure.children, TypeChild{Type: field.Type, Relation: TypeChildStructField})
	}
	return structure
}

func (t *InterfaceType) structure() typeStructure {
	structure := typeStructure{kind: "interface", attributes: []string{strconv.Itoa(len(t.Methods))}}
	for _, method := range t.Methods {
		structure.attributes = append(structure.attributes, method.Name, strconv.Itoa(len(method.Params)))
		for index, param := range method.Params {
			structure.attributes = append(structure.attributes, param.Name)
			relation := TypeChildCallableParameter
			if index == 0 {
				relation = TypeChildMethodReceiver
			}
			structure.children = append(structure.children, TypeChild{Type: param.Type, Relation: relation})
		}
		structure.attributes = appendOriginAttributes(structure.attributes, method.ReturnOrigins)
		structure.children = append(structure.children, TypeChild{Type: method.Return, Relation: TypeChildCallableReturn})
	}
	return structure
}

func (t *EnumType) structure() typeStructure {
	structure := typeStructure{
		kind:       "enum",
		attributes: make([]string, 0, len(t.Cases)),
		children:   make([]TypeChild, 0, len(t.Cases)),
	}
	for _, variant := range t.Cases {
		structure.attributes = append(structure.attributes, variant.Name)
		structure.children = append(structure.children, TypeChild{Type: variant.Payload, Relation: TypeChildEnumPayload})
	}
	return structure
}

func appendOriginAttributes(attributes []string, origins *ReturnOriginContract) []string {
	if origins == nil {
		return append(attributes, "origins:nil")
	}
	attributes = append(attributes, "origins", strconv.Itoa(len(origins.Sources)))
	for _, source := range origins.Sources {
		attributes = append(attributes, strconv.Itoa(source))
	}
	return attributes
}

func (t *InvalidType) withChildren([]TypeChild) Type       { return t }
func (t *UnknownType) withChildren([]TypeChild) Type       { return t }
func (t *IntegerType) withChildren([]TypeChild) Type       { return t }
func (t *ByteType) withChildren([]TypeChild) Type          { return t }
func (t *CharType) withChildren([]TypeChild) Type          { return t }
func (t *FloatType) withChildren([]TypeChild) Type         { return t }
func (t *BoolType) withChildren([]TypeChild) Type          { return t }
func (t *CStrType) withChildren([]TypeChild) Type          { return t }
func (t *StringType) withChildren([]TypeChild) Type        { return t }
func (t *NoneType) withChildren([]TypeChild) Type          { return t }
func (t *AllocatorType) withChildren([]TypeChild) Type     { return t }
func (t *NamedType) withChildren([]TypeChild) Type         { return t }
func (t *TypeParameterType) withChildren([]TypeChild) Type { return t }
func (t *RawPtrType) withChildren([]TypeChild) Type        { return t }
func (t *DefinedType) withChildren([]TypeChild) Type       { return t }
func (*DefinedType) nominal()                              {}

func (t *OwnedPtrType) withChildren(children []TypeChild) Type {
	if t == nil || len(children) != 1 {
		return t
	}
	return &OwnedPtrType{Target: children[0].Type}
}

func (t *RefType) withChildren(children []TypeChild) Type {
	if t == nil || len(children) != 1 {
		return t
	}
	return &RefType{Mutable: t.Mutable, Target: children[0].Type}
}

func (t *OptionalType) withChildren(children []TypeChild) Type {
	if t == nil || len(children) != 1 {
		return t
	}
	return NewOptional(children[0].Type)
}

func (t *ArrayType) withChildren(children []TypeChild) Type {
	if t == nil || len(children) != 1 {
		return t
	}
	return &ArrayType{Len: t.Len, Shape: t.Shape, Elem: children[0].Type}
}

func (t *FuncType) withChildren(children []TypeChild) Type {
	if t == nil || len(children) != len(t.Params)+1 {
		return t
	}
	params := make([]Type, len(t.Params))
	for index := range params {
		params[index] = children[index].Type
	}
	return &FuncType{
		Params:        params,
		ParamNames:    append([]string(nil), t.ParamNames...),
		Return:        children[len(params)].Type,
		ReturnOrigins: t.ReturnOrigins,
	}
}

func (t *StructType) withChildren(children []TypeChild) Type {
	if t == nil || len(children) != len(t.Fields) {
		return t
	}
	fields := make([]Field, len(t.Fields))
	for index, field := range t.Fields {
		fields[index] = Field{Name: field.Name, Type: children[index].Type}
	}
	return &StructType{Fields: fields}
}

func (t *InterfaceType) withChildren(children []TypeChild) Type {
	if t == nil {
		return t
	}
	methods := make([]Method, len(t.Methods))
	childIndex := 0
	for methodIndex, method := range t.Methods {
		methods[methodIndex] = method
		methods[methodIndex].Params = append([]Field(nil), method.Params...)
		for parameterIndex := range method.Params {
			if childIndex >= len(children) {
				return t
			}
			methods[methodIndex].Params[parameterIndex].Type = children[childIndex].Type
			childIndex++
		}
		if childIndex >= len(children) {
			return t
		}
		methods[methodIndex].Return = children[childIndex].Type
		childIndex++
	}
	if childIndex != len(children) {
		return t
	}
	return &InterfaceType{Methods: methods}
}

func (t *EnumType) withChildren(children []TypeChild) Type {
	if t == nil || len(children) != len(t.Cases) {
		return t
	}
	cases := make([]VariantCase, len(t.Cases))
	for index, variant := range t.Cases {
		cases[index] = variant
		cases[index].Payload = children[index].Type
	}
	return &EnumType{Cases: cases}
}
