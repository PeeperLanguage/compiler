package typeinfo

import (
	"strconv"

	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/frontend/ast"
	"compiler/internal/tokens"
)

type Type interface {
	typeNode()
	Text() string
}

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

func (*IntegerType) typeNode() {}
func (*FloatType) typeNode()   {}
func (*BoolType) typeNode()    {}
func (*NamedType) typeNode()   {}

func (t *IntegerType) Text() string {
	if t == nil {
		return ""
	}
	if t.Signed {
		return "i" + itoa(t.Bits)
	}
	return "u" + itoa(t.Bits)
}

func (t *FloatType) Text() string {
	if t == nil {
		return ""
	}
	return "f" + itoa(t.Bits)
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

func itoa(v int) string {
	return strconv.Itoa(v)
}
