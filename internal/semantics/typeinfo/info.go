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

type CopyMode uint8

const (
	CopyInfer CopyMode = iota
	CopyAllow
	CopyDeny
)

var namedTypeCopyModes = map[string]CopyMode{
	"allow_copy": CopyAllow,
	"no_copy":    CopyDeny,
}

func NamedTypeCopyMode(name string) (CopyMode, bool) {
	mode, ok := namedTypeCopyModes[name]
	return mode, ok
}

type DefinedType struct {
	Name       string
	Underlying Type
	CopyMode   CopyMode
}

type OwnedPtrType struct {
	Target Type
}

type RawPtrType struct {
	Target Type
}

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
	Params   []Type
	Consumes []bool
	Return   Type
}

type Field struct {
	Name string
	Type Type
}

type StructType struct {
	Fields []Field
}

type Method struct {
	Name   string
	Params []Field
	Return Type
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
	return "^" + TypeText(t.Target)
}

func (t *RawPtrType) Text() string {
	if t == nil {
		return ""
	}
	return "*" + TypeText(t.Target)
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
		if funcParamConsumes(t, i) {
			b.WriteString("move ")
		}
		b.WriteString(TypeText(param))
	}
	b.WriteString(")")
	if ret := TypeText(t.Return); ret != "" {
		b.WriteString(" -> ")
		b.WriteString(ret)
	}
	return b.String()
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
	b.WriteString("interface{")
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

func funcParamConsumes(fn *FuncType, i int) bool {
	if fn == nil || i < 0 || i >= len(fn.Consumes) {
		return false
	}
	return fn.Consumes[i]
}
