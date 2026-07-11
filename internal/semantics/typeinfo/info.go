package typeinfo

import (
	"compiler/internal/frontend/token"
	"compiler/internal/semantics/symbols"
	"slices"
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

func SameType(left, right Type) bool {
	if left == right {
		return true
	}
	left = Underlying(left)
	right = Underlying(right)
	switch l := left.(type) {
	case *InvalidType:
		_, ok := right.(*InvalidType)
		return ok
	case *UnknownType:
		_, ok := right.(*UnknownType)
		return ok
	case *IntegerType:
		r, ok := right.(*IntegerType)
		return ok && r != nil && l.Signed == r.Signed && l.Bits == r.Bits
	case *BoolType:
		_, ok := right.(*BoolType)
		return ok
	case *CStrType:
		_, ok := right.(*CStrType)
		return ok
	case *StringType:
		_, ok := right.(*StringType)
		return ok
	case *NoneType:
		_, ok := right.(*NoneType)
		return ok
	case *FloatType:
		r, ok := right.(*FloatType)
		return ok && r != nil && l.Bits == r.Bits
	case *NamedType:
		r, ok := right.(*NamedType)
		return ok && r != nil && l.Name == r.Name
	case *OwnedPtrType:
		r, ok := right.(*OwnedPtrType)
		return ok && r != nil && SameType(l.Target, r.Target)
	case *RawPtrType:
		return checkPointerCompatibility(l, right) == Compatible
	case *RefType:
		r, ok := right.(*RefType)
		return ok && r != nil && l.Mutable == r.Mutable && SameType(l.Target, r.Target)
	case *OptionalType:
		r, ok := right.(*OptionalType)
		return ok && r != nil && SameType(l.Inner, r.Inner)
	case *ArrayType:
		r, ok := right.(*ArrayType)
		return ok && r != nil && l.Len == r.Len && l.Dynamic == r.Dynamic && SameType(l.Elem, r.Elem)
	case *FuncType:
		return checkFuncCompatibility(l, right) == Compatible
	case *StructType:
		return checkStructCompatibility(l, right) == Compatible
	case *InterfaceType:
		return checkInterfaceCompatibility(l, right) == Compatible
	case *EnumType:
		return checkEnumCompatibility(l, right) == Compatible
	default:
		return left == nil && right == nil
	}
}

type NumericFamily int

const (
	NumericInvalid NumericFamily = iota
	NumericSigned
	NumericUnsigned
	NumericFloat
)

func NumericInfo(t Type) (family NumericFamily, bits int, ok bool) {
	t = Underlying(t)
	switch typ := t.(type) {
	case *IntegerType:
		if typ == nil {
			return NumericInvalid, 0, false
		}
		if typ.Signed {
			return NumericSigned, typ.Bits, true
		}
		return NumericUnsigned, typ.Bits, true
	case *FloatType:
		if typ == nil {
			return NumericInvalid, 0, false
		}
		return NumericFloat, typ.Bits, true
	case *NamedType:
		if typ == nil {
			return NumericInvalid, 0, false
		}
		if signed, bits, ok := token.ParseIntegerBuiltin(typ.Name); ok {
			if signed {
				return NumericSigned, bits, true
			}
			return NumericUnsigned, bits, true
		}
		switch typ.Name {
		case "f32":
			return NumericFloat, 32, true
		case "f64":
			return NumericFloat, 64, true
		default:
			return NumericInvalid, 0, false
		}
	default:
		return NumericInvalid, 0, false
	}
}

func CommonNumericType(a, b Type) Type {
	if _, _, ok := NumericInfo(a); !ok {
		return nil
	}
	if _, _, ok := NumericInfo(b); !ok {
		return nil
	}
	if SameType(a, b) {
		return a
	}
	if CheckNumericCompatibility(a, b) == Compatible {
		return a
	}
	if CheckNumericCompatibility(b, a) == Compatible {
		return b
	}
	return nil
}

func Assignable(dst, src Type) bool {
	return CheckCompatibility(dst, src) == Compatible
}

func ContainsAbstractSelf(t Type) bool {
	return containsType(t, typeTraversal{followRawPointer: true, followCallable: true}, func(candidate Type, _ bool) bool {
		named, ok := candidate.(*NamedType)
		return ok && named != nil && named.Name == "Self"
	})
}

func ContainsReference(t Type) bool {
	return containsType(t, typeTraversal{followDefined: true}, func(candidate Type, _ bool) bool {
		_, ok := candidate.(*RefType)
		return ok
	})
}

func ContainsStoredReference(t Type) bool {
	return containsType(t, typeTraversal{followDefined: true}, func(candidate Type, stored bool) bool {
		_, ok := candidate.(*RefType)
		return stored && ok
	})
}

type typeTraversal struct {
	followDefined    bool
	followRawPointer bool
	followCallable   bool
}

func containsType(t Type, traversal typeTraversal, matches func(Type, bool) bool) bool {
	type visitKey struct {
		typeValue Type
		stored    bool
	}
	seen := make(map[visitKey]struct{})
	var visit func(Type, bool) bool
	visit = func(current Type, stored bool) bool {
		if current == nil {
			return false
		}
		if matches(current, stored) {
			return true
		}
		key := visitKey{typeValue: current, stored: stored}
		if _, found := seen[key]; found {
			return false
		}
		seen[key] = struct{}{}

		switch typ := current.(type) {
		case *DefinedType:
			return traversal.followDefined && typ != nil && visit(typ.Underlying, stored)
		case *OwnedPtrType:
			return typ != nil && visit(typ.Target, true)
		case *RawPtrType:
			return traversal.followRawPointer && typ != nil && visit(typ.Target, stored)
		case *RefType:
			return typ != nil && visit(typ.Target, stored)
		case *OptionalType:
			return typ != nil && visit(typ.Inner, stored)
		case *ArrayType:
			return typ != nil && visit(typ.Elem, true)
		case *FuncType:
			if typ == nil || !traversal.followCallable {
				return false
			}
			for _, param := range typ.Params {
				if visit(param, false) {
					return true
				}
			}
			return visit(typ.Return, false)
		case *StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if visit(field.Type, true) {
					return true
				}
			}
		case *InterfaceType:
			if typ == nil || !traversal.followCallable {
				return false
			}
			for _, method := range typ.Methods {
				for _, param := range method.Params {
					if visit(param.Type, false) {
						return true
					}
				}
				if visit(method.Return, false) {
					return true
				}
			}
		}
		return false
	}
	return visit(t, false)
}

func ReplaceAbstractSelf(t Type, ownerType Type) Type {
	switch typ := t.(type) {
	case *NamedType:
		if typ != nil && typ.Name == "Self" {
			return ownerType
		}
		return t
	case *OwnedPtrType:
		if typ == nil {
			return nil
		}
		return &OwnedPtrType{Target: ReplaceAbstractSelf(typ.Target, ownerType)}
	case *RawPtrType:
		if typ == nil {
			return nil
		}
		return &RawPtrType{Target: ReplaceAbstractSelf(typ.Target, ownerType)}
	case *RefType:
		if typ == nil {
			return nil
		}
		return &RefType{Mutable: typ.Mutable, Target: ReplaceAbstractSelf(typ.Target, ownerType)}
	case *OptionalType:
		if typ == nil {
			return nil
		}
		return &OptionalType{Inner: ReplaceAbstractSelf(typ.Inner, ownerType)}
	case *ArrayType:
		if typ == nil {
			return nil
		}
		return &ArrayType{Len: typ.Len, Dynamic: typ.Dynamic, Elem: ReplaceAbstractSelf(typ.Elem, ownerType)}
	case *FuncType:
		if typ == nil {
			return nil
		}
		params := make([]Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, ReplaceAbstractSelf(param, ownerType))
		}
		consumes := append([]bool(nil), typ.Consumes...)
		return &FuncType{Params: params, Consumes: consumes, Return: ReplaceAbstractSelf(typ.Return, ownerType)}
	case *StructType:
		if typ == nil {
			return nil
		}
		fields := make([]Field, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			fields = append(fields, Field{Name: field.Name, Type: ReplaceAbstractSelf(field.Type, ownerType)})
		}
		return &StructType{Fields: fields}
	case *InterfaceType:
		if typ == nil {
			return nil
		}
		methods := make([]Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]Field, 0, len(method.Params))
			for _, param := range method.Params {
				params = append(params, Field{Name: param.Name, Type: ReplaceAbstractSelf(param.Type, ownerType)})
			}
			methods = append(methods, Method{
				Name:   method.Name,
				Params: params,
				Return: ReplaceAbstractSelf(method.Return, ownerType),
			})
		}
		return &InterfaceType{Methods: methods}
	default:
		return t
	}
}

func IsInvalid(typ Type) bool {
	_, ok := typ.(*InvalidType)
	return ok
}

func IsUnknown(typ Type) bool {
	_, ok := typ.(*UnknownType)
	return ok
}

// isInvalidOrUnknown replaces the repeated `typeinfo.IsInvalid(t) || typeinfo.IsUnknown(t)` pattern.
func IsInvalidOrUnknown(t Type) bool {
	return IsInvalid(t) || IsUnknown(t)
}

type Expr interface {
	Type() Type
}

type IntLit struct {
	Value    string
	ExprType Type
}

func (e *IntLit) Type() Type {
	if e == nil {
		return nil
	}
	return e.ExprType
}

type Ident struct {
	Symbol   *symbols.Symbol
	ExprType Type
}

func (e *Ident) Type() Type {
	if e == nil {
		return nil
	}
	return e.ExprType
}

type Unary struct {
	Op       string
	Arg      Expr
	ExprType Type
}

func (e *Unary) Type() Type {
	if e == nil {
		return nil
	}
	return e.ExprType
}

type Binary struct {
	Op       string
	Left     Expr
	Right    Expr
	ExprType Type
}

func (e *Binary) Type() Type {
	if e == nil {
		return nil
	}
	return e.ExprType
}

type Call struct {
	Callee   Expr
	Args     []Expr
	ExprType Type
}

func (e *Call) Type() Type {
	if e == nil {
		return nil
	}
	return e.ExprType
}

type FloatLit struct {
	Value    string
	ExprType Type
}

func (e *FloatLit) Type() Type {
	if e == nil {
		return nil
	}
	return e.ExprType
}

type As struct {
	Expr     Expr
	CastType Type
	ExprType Type
}

func (e *As) Type() Type {
	if e == nil {
		return nil
	}
	return e.ExprType
}

func GetMethodLookupKeys(baseType Type) []string {

	keys := make([]string, 0, 4)
	appendKey := func(typ Type) {
		if typ == nil {
			return
		}
		key := TypeText(typ)
		if key == "" {
			return
		}
		if slices.Contains(keys, key) {
			return
		}
		keys = append(keys, key)
	}
	appendType := func(typ Type) {
		appendKey(typ)
		if underlying := Underlying(typ); underlying != typ {
			appendKey(underlying)
		}
	}
	appendType(baseType)
	if target, ok := PointerTarget(baseType); ok {
		appendType(target)
	}
	if target, _, ok := ReferenceTarget(Underlying(baseType)); ok {
		appendType(target)
	}
	return keys
}

func PointerTarget(t Type) (Type, bool) {
	switch ptr := t.(type) {
	case *OwnedPtrType:
		if ptr != nil && ptr.Target != nil {
			return ptr.Target, true
		}
	case *RawPtrType:
		if ptr != nil && ptr.Target != nil {
			return ptr.Target, true
		}
	}
	return nil, false
}

func RawPointerTarget(t Type) (Type, bool) {
	ptr, ok := t.(*RawPtrType)
	if !ok || ptr == nil || ptr.Target == nil {
		return nil, false
	}
	return ptr.Target, true
}

func ReferenceTarget(t Type) (target Type, mutable bool, ok bool) {
	ref, ok := t.(*RefType)
	if !ok || ref == nil || ref.Target == nil {
		return nil, false, false
	}
	return ref.Target, ref.Mutable, true
}

// InterfaceTypeOf recognizes both owned interface values and borrowed
// interface views so semantic lookup and lowering share one type boundary.
func InterfaceTypeOf(t Type) (*InterfaceType, bool) {
	if target, _, ok := ReferenceTarget(Underlying(t)); ok {
		t = target
	}
	iface, ok := Underlying(t).(*InterfaceType)
	return iface, ok && iface != nil
}

// LookupStructField centralizes field search so checker and lowerer agree on
// struct layout. Checker needs the field type for validation; lowerer needs
// the same field index to emit field access.
func LookupStructField(baseType Type, name string) (field Field, index int, ok bool) {
	if baseType == nil || name == "" {
		return Field{}, -1, false
	}
	if target, ptrOK := PointerTarget(baseType); ptrOK {
		baseType = target
	} else if target, _, refOK := ReferenceTarget(Underlying(baseType)); refOK {
		baseType = target
	}
	strct, ok := Underlying(baseType).(*StructType)
	if !ok || strct == nil {
		return Field{}, -1, false
	}
	for i, candidate := range strct.Fields {
		if candidate.Name == name {
			return candidate, i, true
		}
	}
	return Field{}, -1, false
}
