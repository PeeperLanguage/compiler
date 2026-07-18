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

type FloatType struct {
	Bits int
}

type BoolType struct{}

type CStrType struct{}

type StringType struct{}

type NoneType struct{}

type NamedType struct {
	Name string
}

type DefinedType struct {
	Name       string
	Underlying Type
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

type ArrayType struct {
	Len     string
	Dynamic bool
	Elem    Type
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
	Variants []string
}

func (*InvalidType) TypeNode()   {}
func (*UnknownType) TypeNode()   {}
func (*IntegerType) TypeNode()   {}
func (*ByteType) TypeNode()      {}
func (*FloatType) TypeNode()     {}
func (*BoolType) TypeNode()      {}
func (*CStrType) TypeNode()      {}
func (*StringType) TypeNode()    {}
func (*NoneType) TypeNode()      {}
func (*NamedType) TypeNode()     {}
func (*DefinedType) TypeNode()   {}
func (*OwnedPtrType) TypeNode()  {}
func (*RawPtrType) TypeNode()    {}
func (*RefType) TypeNode()       {}
func (*OptionalType) TypeNode()  {}
func (*ArrayType) TypeNode()     {}
func (*FuncType) TypeNode()      {}
func (*StructType) TypeNode()    {}
func (*InterfaceType) TypeNode() {}
func (*EnumType) TypeNode()      {}

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

func (t *FloatType) Text() string {
	if t == nil {
		return ""
	}
	return "f" + strconv.Itoa(t.Bits)
}

func (*BoolType) Text() string { return "bool" }

func (*CStrType) Text() string { return "cstr" }

func (*StringType) Text() string { return "string" }

func (*NoneType) Text() string { return "none" }

func (t *NamedType) Text() string {
	if t == nil {
		return ""
	}
	return t.Name
}

func (t *DefinedType) Text() string {
	if t == nil {
		return ""
	}
	return t.Name
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
	if t.Dynamic {
		return "[]" + TypeText(t.Elem)
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
	return "enum{" + strings.Join(t.Variants, ", ") + "}"
}

func TypeText(typ Type) string {
	if typ == nil {
		return ""
	}
	return typ.Text()
}
