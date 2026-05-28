package typeinfo

import (
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/frontend/ast"
	"compiler/internal/tokens"
	"strconv"
)

type Type interface {
	typeNode()
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

type NamedType struct {
	Name string
}

func (*InvalidType) typeNode() {}
func (*UnknownType) typeNode() {}
func (*IntegerType) typeNode() {}
func (*FloatType) typeNode()   {}
func (*BoolType) typeNode()    {}
func (*NamedType) typeNode()   {}

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

func (t *NamedType) Text() string {
	if t == nil {
		return ""
	}
	return t.Name
}

func TypeText(typ Type) string {
	if typ == nil {
		return ""
	}
	return typ.Text()
}

func TypeFromSyntax(node ast.TypeExpr) Type {
	switch typ := node.(type) {
	case *ast.NamedType:
		if typ == nil {
			return nil
		}
		if typ.Name == "bool" {
			return &BoolType{}
		}
		if typ.Name == "f32" {
			return &FloatType{Bits: 32}
		}
		if typ.Name == "f64" {
			return &FloatType{Bits: 64}
		}
		if signed, bits, ok := tokens.ParseIntegerBuiltin(typ.Name); ok {
			return &IntegerType{Signed: signed, Bits: bits}
		}
		return &NamedType{Name: typ.Name}
	default:
		return nil
	}
}

func IsI32(typ Type) bool {
	intType, ok := typ.(*IntegerType)
	return ok && intType != nil && intType.Signed && intType.Bits == 32
}

func SameType(left, right Type) bool {
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
	case *FloatType:
		r, ok := right.(*FloatType)
		return ok && r != nil && l.Bits == r.Bits
	case *NamedType:
		r, ok := right.(*NamedType)
		return ok && r != nil && l.Name == r.Name
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
		if signed, bits, ok := tokens.ParseIntegerBuiltin(typ.Name); ok {
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
	if dst == nil || src == nil {
		return true
	}
	if IsInvalid(dst) || IsInvalid(src) || IsUnknown(dst) || IsUnknown(src) {
		return true
	}
	// Check numeric compatibility for implicit conversions
	if compat := CheckNumericCompatibility(dst, src); compat == Compatible {
		return true
	}
	return SameType(dst, src)
}

func IsInvalid(typ Type) bool {
	_, ok := typ.(*InvalidType)
	return ok
}

func IsUnknown(typ Type) bool {
	_, ok := typ.(*UnknownType)
	return ok
}

type ModuleInfo struct {
	Externs         []declinfo.ExternDecl
	Exprs           map[ast.Expr]Expr
	SymbolTypes     map[symbols.SymbolID]Type
	FunctionReturns map[*ast.FnDecl]Type
}

func NewModuleInfo() *ModuleInfo {
	return &ModuleInfo{
		Externs:         make([]declinfo.ExternDecl, 0),
		Exprs:           make(map[ast.Expr]Expr),
		SymbolTypes:     make(map[symbols.SymbolID]Type),
		FunctionReturns: make(map[*ast.FnDecl]Type),
	}
}

func (m *ModuleInfo) BindExpr(node ast.Expr, expr Expr) {
	if m == nil || node == nil || expr == nil {
		return
	}
	m.Exprs[node] = expr
}

func (m *ModuleInfo) LookupExpr(node ast.Expr) (Expr, bool) {
	if m == nil || node == nil {
		return nil, false
	}
	expr, ok := m.Exprs[node]
	return expr, ok
}

func (m *ModuleInfo) BindSymbolType(sym *symbols.Symbol, typ Type) {
	if m == nil || sym == nil || typ == nil {
		return
	}
	m.SymbolTypes[sym.ID] = typ
}

func (m *ModuleInfo) LookupSymbolType(sym *symbols.Symbol) (Type, bool) {
	if m == nil || sym == nil {
		return nil, false
	}
	typ, ok := m.SymbolTypes[sym.ID]
	return typ, ok
}

func (m *ModuleInfo) BindFunctionReturn(fn *ast.FnDecl, typ Type) {
	if m == nil || fn == nil || typ == nil {
		return
	}
	m.FunctionReturns[fn] = typ
}

func (m *ModuleInfo) LookupFunctionReturn(fn *ast.FnDecl) (Type, bool) {
	if m == nil || fn == nil {
		return nil, false
	}
	typ, ok := m.FunctionReturns[fn]
	return typ, ok
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
