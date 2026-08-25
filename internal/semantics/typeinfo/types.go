package typeinfo

import (
	"strconv"
	"strings"
)

type Type interface {
	TypeNode()
	Text() string
}

type InvalidType struct{}

type UnknownType struct{}

type IntegerType struct {
	Signed bool
	Bits   int
}

type ByteType struct{}

type CharType struct{}

type FloatType struct {
	Bits int
}

type BoolType struct{}

type CStrType struct{}

type StringType struct{}

type NoneType struct{}

type AllocatorType struct{}

type NamedType struct {
	Name string
}

type DefinedKind uint8

const (
	DefinedKindInvalid DefinedKind = iota
	DefinedKindAlias
	DefinedKindStruct
	DefinedKindInterface
	DefinedKindEnum
)

type TypeParameterType struct {
	Name          string
	OwnerIdentity string
	Index         int
}

type DefinedType struct {
	Name           string
	Identity       string
	Kind           DefinedKind
	TypeParameters []*TypeParameterType
	TypeArguments  []Type
	Underlying     Type
}

type OwnedPtrType struct {
	Target Type
}

type RawPtrType struct{}

type RefType struct {
	Mutable bool
	Target  Type
}

type OptionalType struct {
	Inner Type
}

type VariantFamily uint8

const (
	VariantFamilyInvalid VariantFamily = iota
	VariantFamilyOptional
	VariantFamilyNamed
)

type VariantCase struct {
	Name    string
	Payload Type
}

type VariantDescriptor struct {
	Family   VariantFamily
	Identity string
	Cases    []VariantCase
}

type ArrayShape uint8

const (
	ArrayFixed ArrayShape = iota
	ArrayOwner
	ArraySlice
)

type ArrayType struct {
	Len   string
	Shape ArrayShape
	Elem  Type
}

type FuncType struct {
	Params        []Type
	ParamNames    []string
	Return        Type
	ReturnOrigins *ReturnOriginContract
}

type ReturnOriginContract struct {
	Sources []int
}

type Field struct {
	Name string
	Type Type
}

type StructType struct {
	Fields []Field
}

type Method struct {
	Name          string
	Params        []Field
	Return        Type
	ReturnOrigins *ReturnOriginContract
}

func (m Method) CallableType() *FuncType {
	params := make([]Type, len(m.Params))
	paramNames := make([]string, len(m.Params))
	for i, param := range m.Params {
		params[i] = param.Type
		paramNames[i] = param.Name
	}
	return &FuncType{
		Params:        params,
		ParamNames:    paramNames,
		Return:        m.Return,
		ReturnOrigins: m.ReturnOrigins,
	}
}

type InterfaceType struct {
	Methods []Method
}

type EnumType struct {
	Cases []VariantCase
}

func (*InvalidType) TypeNode()       {}
func (*UnknownType) TypeNode()       {}
func (*IntegerType) TypeNode()       {}
func (*ByteType) TypeNode()          {}
func (*CharType) TypeNode()          {}
func (*FloatType) TypeNode()         {}
func (*BoolType) TypeNode()          {}
func (*CStrType) TypeNode()          {}
func (*StringType) TypeNode()        {}
func (*NoneType) TypeNode()          {}
func (*AllocatorType) TypeNode()     {}
func (*NamedType) TypeNode()         {}
func (*TypeParameterType) TypeNode() {}
func (*DefinedType) TypeNode()       {}
func (*OwnedPtrType) TypeNode()      {}
func (*RawPtrType) TypeNode()        {}
func (*RefType) TypeNode()           {}
func (*OptionalType) TypeNode()      {}
func (*ArrayType) TypeNode()         {}
func (*FuncType) TypeNode()          {}
func (*StructType) TypeNode()        {}
func (*InterfaceType) TypeNode()     {}
func (*EnumType) TypeNode()          {}

func (*InvalidType) Text() string { return "<invalid>" }
func (*UnknownType) Text() string { return "<unknown>" }

func (t *IntegerType) Text() string {
	if t == nil {
		return ""
	}
	if t.Signed {
		return "i" + strconv.Itoa(t.Bits)
	}
	return "u" + strconv.Itoa(t.Bits)
}

func (*ByteType) Text() string { return "byte" }

func (*CharType) Text() string { return "char" }

func (t *FloatType) Text() string {
	if t == nil {
		return ""
	}
	return "f" + strconv.Itoa(t.Bits)
}

func (*BoolType) Text() string { return "bool" }

func (*CStrType) Text() string { return "cstr" }

func (*StringType) Text() string { return "str" }

func (*NoneType) Text() string { return "none" }

func (*AllocatorType) Text() string { return "Allocator" }

func (t *NamedType) Text() string {
	if t == nil {
		return ""
	}
	return t.Name
}

func (t *TypeParameterType) Text() string {
	if t == nil {
		return ""
	}
	return t.Name
}

func (t *DefinedType) Text() string {
	if t == nil {
		return ""
	}
	arguments := t.TypeArguments
	if len(arguments) == 0 && len(t.TypeParameters) > 0 {
		arguments = make([]Type, len(t.TypeParameters))
		for index, parameter := range t.TypeParameters {
			arguments[index] = parameter
		}
	}
	if len(arguments) == 0 {
		return t.Name
	}
	parts := make([]string, len(arguments))
	for index, argument := range arguments {
		parts[index] = TypeText(argument)
	}
	return t.Name + "<" + strings.Join(parts, ", ") + ">"
}

// TypeParameterBindings is the canonical substitution environment for one
// named declaration. Nil arguments retain declaration parameters; concrete
// arguments replace them during instance construction.
func TypeParameterBindings(parameters []*TypeParameterType, arguments []Type) map[string]Type {
	bindings := make(map[string]Type, len(parameters))
	for index, parameter := range parameters {
		if parameter == nil || parameter.Name == "" {
			continue
		}
		bound := Type(parameter)
		if len(arguments) == len(parameters) && arguments[index] != nil {
			bound = arguments[index]
		}
		bindings[parameter.Name] = bound
	}
	return bindings
}

// Unalias returns canonical transparent-alias storage without erasing nominal
// structs, interfaces, or enums. Invalid alias cycles terminate as invalid.
func Unalias(t Type) Type {
	seen := make(map[*DefinedType]struct{})
	for {
		defined, ok := t.(*DefinedType)
		if !ok || defined == nil || defined.Kind != DefinedKindAlias || defined.Underlying == nil {
			return t
		}
		if _, found := seen[defined]; found {
			return &InvalidType{}
		}
		seen[defined] = struct{}{}
		t = defined.Underlying
	}
}

func Underlying(t Type) Type {
	for {
		defined, ok := t.(*DefinedType)
		if !ok || defined == nil || defined.Underlying == nil {
			return t
		}
		t = defined.Underlying
	}
}

// VariantDescriptorOf is source semantics' canonical variant classification.
// It preserves nominal identity before inspecting a defined enum's underlying
// representation, while optionals remain structural source types.
func VariantDescriptorOf(t Type) (VariantDescriptor, bool) {
	identity := ""
	if enumIdentity, nominal := nominalEnumIdentity(t); nominal {
		identity = enumIdentity
	} else if defined, ok := t.(*DefinedType); ok && defined != nil && defined.Kind != DefinedKindAlias {
		identity = defined.Identity
		if identity == "" {
			identity = defined.Name
		}
	}
	t = Underlying(t)
	switch variant := t.(type) {
	case *OptionalType:
		if variant == nil || variant.Inner == nil {
			return VariantDescriptor{}, false
		}
		return VariantDescriptor{
			Family: VariantFamilyOptional,
			Cases: []VariantCase{
				{Name: "Absent"},
				{Name: "Present", Payload: variant.Inner},
			},
		}, true
	case *EnumType:
		if variant == nil || len(variant.Cases) == 0 {
			return VariantDescriptor{}, false
		}
		if identity == "" {
			identity = variant.Text()
		}
		cases := append([]VariantCase(nil), variant.Cases...)
		return VariantDescriptor{Family: VariantFamilyNamed, Identity: identity, Cases: cases}, true
	default:
		return VariantDescriptor{}, false
	}
}

func (t *OwnedPtrType) Text() string {
	if t == nil {
		return ""
	}
	return "*" + TypeText(t.Target)
}

func (t *RawPtrType) Text() string {
	if t == nil {
		return ""
	}
	return "rawptr"
}

func (t *RefType) Text() string {
	if t == nil {
		return ""
	}
	prefix := "&"
	if t.Mutable {
		prefix = "&mut "
	}
	return prefix + TypeText(t.Target)
}

func (t *OptionalType) Text() string {
	if t == nil {
		return ""
	}
	return "?" + TypeText(t.Inner)
}

func (t *ArrayType) Text() string {
	if t == nil {
		return ""
	}
	switch t.Shape {
	case ArrayOwner:
		return "[]" + TypeText(t.Elem)
	case ArraySlice:
		return "[..]" + TypeText(t.Elem)
	}
	if t.Len == "" {
		return "[" + TypeText(t.Elem) + "]"
	}
	return "[" + t.Len + "]" + TypeText(t.Elem)
}

func (t *FuncType) Text() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("fn(")
	for i, param := range t.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(TypeText(param))
	}
	b.WriteString(")")
	if ret := TypeText(t.Return); ret != "" {
		b.WriteString(" -> ")
		b.WriteString(ret)
	}
	b.WriteString(t.ReturnOriginText())
	return b.String()
}

func (t *FuncType) ReturnOriginText() string {
	if t != nil && t.ReturnOrigins != nil && len(t.ReturnOrigins.Sources) > 0 {
		var b strings.Builder
		b.WriteString(" from ")
		if len(t.ReturnOrigins.Sources) > 1 {
			b.WriteString("(")
		}
		for i, slot := range t.ReturnOrigins.Sources {
			if i > 0 {
				b.WriteString(", ")
			}
			if slot >= 0 && slot < len(t.ParamNames) && t.ParamNames[slot] != "" {
				b.WriteString(t.ParamNames[slot])
			} else {
				b.WriteString("?")
			}
		}
		if len(t.ReturnOrigins.Sources) > 1 {
			b.WriteString(")")
		}
		return b.String()
	}
	return ""
}

func (t *StructType) Text() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("struct{")
	for i, field := range t.Fields {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(field.Name)
		b.WriteString(": ")
		b.WriteString(TypeText(field.Type))
	}
	b.WriteString("}")
	return b.String()
}

func (t *InterfaceType) Text() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("iface{")
	for i, method := range t.Methods {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(method.Name)
		b.WriteString("(")
		for j, param := range method.Params {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(param.Name)
			if param.Name != "" {
				b.WriteString(": ")
			}
			b.WriteString(TypeText(param.Type))
		}
		b.WriteString(")")
		if ret := TypeText(method.Return); ret != "" {
			b.WriteString(": ")
			b.WriteString(ret)
		}
	}
	b.WriteString("}")
	return b.String()
}

func (t *EnumType) Text() string {
	if t == nil {
		return ""
	}
	cases := make([]string, len(t.Cases))
	for index, variant := range t.Cases {
		cases[index] = variant.Name
		if variant.Payload != nil {
			cases[index] += ": " + TypeText(variant.Payload)
		}
	}
	return "enum{" + strings.Join(cases, ", ") + "}"
}

func TypeText(typ Type) string {
	if typ == nil {
		return ""
	}
	return typ.Text()
}
