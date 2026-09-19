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

// typeDescription is the canonical local description of one semantic type.
// Semantic identity consumes all fields; structural analyses project only
// present children through ForEachChild.
type typeDescription struct {
	kind       string
	attributes []string
	children   []TypeChild
}

func describeType(kind string, attributes ...string) typeDescription {
	return typeDescription{kind: kind, attributes: attributes}
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
	for _, child := range typ.description().children {
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
	children := typ.description().children
	if len(children) == 0 {
		return typ
	}
	transformed := make([]TypeChild, len(children))
	for index, child := range children {
		transformed[index] = child
		transformed[index].Type = transform(child)
	}
	return typ.rebuildChildren(transformed)
}

type nominalType interface {
	Type
	nominal()
}

func (*InvalidType) description() typeDescription   { return describeType("invalid") }
func (*UnknownType) description() typeDescription   { return describeType("unknown") }
func (*ByteType) description() typeDescription      { return describeType("byte") }
func (*CharType) description() typeDescription      { return describeType("char") }
func (*BoolType) description() typeDescription      { return describeType("bool") }
func (*CStrType) description() typeDescription      { return describeType("cstr") }
func (*StringType) description() typeDescription    { return describeType("string") }
func (*NoneType) description() typeDescription      { return describeType("none") }
func (*AllocatorType) description() typeDescription { return describeType("allocator") }
func (*RawPtrType) description() typeDescription    { return describeType("rawptr") }

func (t *IntegerType) description() typeDescription {
	return describeType("integer", strconv.FormatBool(t.Signed), strconv.Itoa(t.Bits))
}

func (t *FloatType) description() typeDescription {
	return describeType("float", strconv.Itoa(t.Bits))
}

func (t *NamedType) description() typeDescription {
	return describeType("named", t.Name)
}

func (t *TypeParameterType) description() typeDescription {
	return describeType("parameter", t.OwnerIdentity, strconv.Itoa(t.Index), t.Name)
}

func (t *DefinedType) description() typeDescription {
	description := describeType("defined", strconv.Itoa(int(t.Kind)), t.Identity, t.Name)
	description.children = make([]TypeChild, 0, 1+len(t.TypeParameters)+len(t.TypeArguments))
	description.children = append(description.children, TypeChild{Type: t.Underlying, Relation: TypeChildUnderlying})
	for _, parameter := range t.TypeParameters {
		description.children = append(description.children, TypeChild{Type: parameter, Relation: TypeChildTypeParameter})
	}
	for _, argument := range t.TypeArguments {
		description.children = append(description.children, TypeChild{Type: argument, Relation: TypeChildTypeArgument})
	}
	return description
}

func (t *OwnedPtrType) description() typeDescription {
	return typeDescription{kind: "owned", children: []TypeChild{{Type: t.Target, Relation: TypeChildOwnedTarget}}}
}

func (t *RefType) description() typeDescription {
	return typeDescription{
		kind:       "ref",
		attributes: []string{strconv.FormatBool(t.Mutable)},
		children:   []TypeChild{{Type: t.Target, Relation: TypeChildBorrowedTarget}},
	}
}

func (t *OptionalType) description() typeDescription {
	return typeDescription{kind: "optional", children: []TypeChild{{Type: t.Inner, Relation: TypeChildOptionalPayload}}}
}

func (t *ArrayType) description() typeDescription {
	return typeDescription{
		kind:       "array",
		attributes: []string{strconv.Itoa(int(t.Shape)), t.Len},
		children:   []TypeChild{{Type: t.Elem, Relation: TypeChildArrayElement}},
	}
}

func (t *FuncType) description() typeDescription {
	description := typeDescription{
		kind:       "func",
		attributes: make([]string, 0, len(t.Params)+2),
		children:   make([]TypeChild, 0, len(t.Params)+1),
	}
	for index, param := range t.Params {
		name := ""
		if index < len(t.ParamNames) {
			name = t.ParamNames[index]
		}
		description.attributes = append(description.attributes, name)
		description.children = append(description.children, TypeChild{Type: param, Relation: TypeChildCallableParameter})
	}
	description.attributes = appendOriginAttributes(description.attributes, t.ReturnOrigins)
	description.children = append(description.children, TypeChild{Type: t.Return, Relation: TypeChildCallableReturn})
	return description
}

func (t *StructType) description() typeDescription {
	description := typeDescription{
		kind:       "struct",
		attributes: make([]string, 0, len(t.Fields)),
		children:   make([]TypeChild, 0, len(t.Fields)),
	}
	for _, field := range t.Fields {
		description.attributes = append(description.attributes, field.Name)
		description.children = append(description.children, TypeChild{Type: field.Type, Relation: TypeChildStructField})
	}
	return description
}

func (t *InterfaceType) description() typeDescription {
	description := typeDescription{kind: "interface", attributes: []string{strconv.Itoa(len(t.Methods))}}
	for _, method := range t.Methods {
		description.attributes = append(description.attributes, method.Name, strconv.Itoa(len(method.Params)))
		for index, param := range method.Params {
			description.attributes = append(description.attributes, param.Name)
			relation := TypeChildCallableParameter
			if index == 0 {
				relation = TypeChildMethodReceiver
			}
			description.children = append(description.children, TypeChild{Type: param.Type, Relation: relation})
		}
		description.attributes = appendOriginAttributes(description.attributes, method.ReturnOrigins)
		description.children = append(description.children, TypeChild{Type: method.Return, Relation: TypeChildCallableReturn})
	}
	return description
}

func (t *EnumType) description() typeDescription {
	description := typeDescription{
		kind:       "enum",
		attributes: make([]string, 0, len(t.Cases)),
		children:   make([]TypeChild, 0, len(t.Cases)),
	}
	for _, variant := range t.Cases {
		description.attributes = append(description.attributes, variant.Name)
		description.children = append(description.children, TypeChild{Type: variant.Payload, Relation: TypeChildEnumPayload})
	}
	return description
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

func (t *InvalidType) rebuildChildren([]TypeChild) Type       { return t }
func (t *UnknownType) rebuildChildren([]TypeChild) Type       { return t }
func (t *IntegerType) rebuildChildren([]TypeChild) Type       { return t }
func (t *ByteType) rebuildChildren([]TypeChild) Type          { return t }
func (t *CharType) rebuildChildren([]TypeChild) Type          { return t }
func (t *FloatType) rebuildChildren([]TypeChild) Type         { return t }
func (t *BoolType) rebuildChildren([]TypeChild) Type          { return t }
func (t *CStrType) rebuildChildren([]TypeChild) Type          { return t }
func (t *StringType) rebuildChildren([]TypeChild) Type        { return t }
func (t *NoneType) rebuildChildren([]TypeChild) Type          { return t }
func (t *AllocatorType) rebuildChildren([]TypeChild) Type     { return t }
func (t *NamedType) rebuildChildren([]TypeChild) Type         { return t }
func (t *TypeParameterType) rebuildChildren([]TypeChild) Type { return t }
func (t *RawPtrType) rebuildChildren([]TypeChild) Type        { return t }
func (t *DefinedType) rebuildChildren([]TypeChild) Type       { return t }
func (*DefinedType) nominal()                                 {}

func (t *OwnedPtrType) rebuildChildren(children []TypeChild) Type {
	if t == nil || len(children) != 1 {
		return t
	}
	return &OwnedPtrType{Target: children[0].Type}
}

func (t *RefType) rebuildChildren(children []TypeChild) Type {
	if t == nil || len(children) != 1 {
		return t
	}
	return &RefType{Mutable: t.Mutable, Target: children[0].Type}
}

func (t *OptionalType) rebuildChildren(children []TypeChild) Type {
	if t == nil || len(children) != 1 {
		return t
	}
	return NewOptional(children[0].Type)
}

func (t *ArrayType) rebuildChildren(children []TypeChild) Type {
	if t == nil || len(children) != 1 {
		return t
	}
	return &ArrayType{Len: t.Len, Shape: t.Shape, Elem: children[0].Type}
}

func (t *FuncType) rebuildChildren(children []TypeChild) Type {
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

func (t *StructType) rebuildChildren(children []TypeChild) Type {
	if t == nil || len(children) != len(t.Fields) {
		return t
	}
	fields := make([]Field, len(t.Fields))
	for index, field := range t.Fields {
		fields[index] = Field{Name: field.Name, Type: children[index].Type}
	}
	return &StructType{Fields: fields}
}

func (t *InterfaceType) rebuildChildren(children []TypeChild) Type {
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

func (t *EnumType) rebuildChildren(children []TypeChild) Type {
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
