package typeinfo

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
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

type NamedType struct {
	Name string
}

type DefinedType struct {
	Name       string
	Underlying Type
}

type RawPtrType struct {
	Mutable bool
	Target  Type
}

type FuncType struct {
	Params []Type
	Return Type
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
func (*NamedType) TypeNode()     {}
func (*DefinedType) TypeNode()   {}
func (*RawPtrType) TypeNode()    {}
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

func (t *RawPtrType) Text() string {
	if t == nil {
		return ""
	}
	return "^" + TypeText(t.Target)
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

func TypeFromSyntax(node ast.TypeExpr) Type {
	switch typ := node.(type) {
	case *ast.ScopeResolution:
		if typ == nil {
			return nil
		}
		return &NamedType{Name: typ.Module.Name + "::" + typ.Name.Name}
	case *ast.NamedType:
		if typ == nil {
			return nil
		}
		if typ.Name == "bool" {
			return &BoolType{}
		}
		if typ.Name == "cstr" {
			return &CStrType{}
		}
		if typ.Name == "f32" {
			return &FloatType{Bits: 32}
		}
		if typ.Name == "f64" {
			return &FloatType{Bits: 64}
		}
		if signed, bits, ok := token.ParseIntegerBuiltin(typ.Name); ok {
			return &IntegerType{Signed: signed, Bits: bits}
		}
		return &NamedType{Name: typ.Name}
	case *ast.RawPtrType:
		if typ == nil {
			return nil
		}
		return &RawPtrType{Mutable: typ.Mutable, Target: TypeFromSyntax(typ.Target)}
	case *ast.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, TypeFromSyntax(param))
		}
		return &FuncType{
			Params: params,
			Return: TypeFromSyntax(typ.Return),
		}
	case *ast.StructType:
		if typ == nil {
			return nil
		}
		fields := make([]Field, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			name := ""
			if field.Name != nil {
				name = field.Name.Name
			}
			fields = append(fields, Field{Name: name, Type: TypeFromSyntax(field.Type)})
		}
		return &StructType{Fields: fields}
	case *ast.InterfaceType:
		if typ == nil {
			return nil
		}
		methods := make([]Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]Field, 0, len(method.Params))
			for _, param := range method.Params {
				name := ""
				if param.Name != nil {
					name = param.Name.Name
				}
				params = append(params, Field{Name: name, Type: TypeFromSyntax(param.Type)})
			}
			name := ""
			if method.Name != nil {
				name = method.Name.Name
			}
			methods = append(methods, Method{
				Name:   name,
				Params: params,
				Return: TypeFromSyntax(method.ReturnType),
			})
		}
		return &InterfaceType{Methods: methods}
	case *ast.EnumType:
		if typ == nil {
			return nil
		}
		variants := make([]string, 0, len(typ.Variants))
		for _, variant := range typ.Variants {
			if variant.Name != nil {
				variants = append(variants, variant.Name.Name)
			}
		}
		return &EnumType{Variants: variants}
	default:
		return nil
	}
}

func IsI32(typ Type) bool {
	intType, ok := typ.(*IntegerType)
	return ok && intType != nil && intType.Signed && intType.Bits == 32
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
	case *FloatType:
		r, ok := right.(*FloatType)
		return ok && r != nil && l.Bits == r.Bits
	case *NamedType:
		r, ok := right.(*NamedType)
		return ok && r != nil && l.Name == r.Name
	case *RawPtrType:
		return checkPointerCompatibility(l, right) == Compatible
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
	// Use the new compatibility system
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
	switch typ := t.(type) {
	case *NamedType:
		return typ != nil && typ.Name == "Self"
	case *RawPtrType:
		return typ != nil && ContainsAbstractSelf(typ.Target)
	case *FuncType:
		if typ == nil {
			return false
		}
		if slices.ContainsFunc(typ.Params, ContainsAbstractSelf) {
			return true
		}
		return ContainsAbstractSelf(typ.Return)
	case *StructType:
		if typ == nil {
			return false
		}
		for _, field := range typ.Fields {
			if ContainsAbstractSelf(field.Type) {
				return true
			}
		}
		return false
	case *InterfaceType:
		if typ == nil {
			return false
		}
		for _, method := range typ.Methods {
			for _, param := range method.Params {
				if ContainsAbstractSelf(param.Type) {
					return true
				}
			}
			if ContainsAbstractSelf(method.Return) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func ReplaceAbstractSelf(t Type, ownerType Type) Type {
	switch typ := t.(type) {
	case *NamedType:
		if typ != nil && typ.Name == "Self" {
			return ownerType
		}
		return t
	case *RawPtrType:
		if typ == nil {
			return nil
		}
		return &RawPtrType{Mutable: typ.Mutable, Target: ReplaceAbstractSelf(typ.Target, ownerType)}
	case *FuncType:
		if typ == nil {
			return nil
		}
		params := make([]Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, ReplaceAbstractSelf(param, ownerType))
		}
		return &FuncType{Params: params, Return: ReplaceAbstractSelf(typ.Return, ownerType)}
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


func ResolveTypeWithScope(scope *table.Scope, t Type) Type {
	if scope == nil || t == nil {
		return t
	}
	if named, ok := t.(*NamedType); ok && named != nil {
		sym, found := scope.Lookup(named.Name)
		if found && sym != nil && sym.Kind == symbols.SymbolType {
			if resolved, ok := symbols.GetSymbolType(sym); ok && resolved != nil {
				return resolved
			}
		}
	}
	return t
}