package typeinfo

import (
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/frontend/ast"
)

type ModuleInfo struct {
	Externs         []declinfo.ExternDecl
	Exprs           map[ast.Expr]Expr
	SymbolTypes     map[symbols.SymbolID]string
	FunctionReturns map[*ast.FnDecl]string
}

func NewModuleInfo() *ModuleInfo {
	return &ModuleInfo{
		Externs:         make([]declinfo.ExternDecl, 0),
		Exprs:           make(map[ast.Expr]Expr),
		SymbolTypes:     make(map[symbols.SymbolID]string),
		FunctionReturns: make(map[*ast.FnDecl]string),
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

func (m *ModuleInfo) BindSymbolType(sym *symbols.Symbol, name string) {
	if m == nil || sym == nil || name == "" {
		return
	}
	m.SymbolTypes[sym.ID] = name
}

func (m *ModuleInfo) LookupSymbolType(sym *symbols.Symbol) (string, bool) {
	if m == nil || sym == nil {
		return "", false
	}
	name, ok := m.SymbolTypes[sym.ID]
	return name, ok
}

func (m *ModuleInfo) BindFunctionReturn(fn *ast.FnDecl, name string) {
	if m == nil || fn == nil || name == "" {
		return
	}
	m.FunctionReturns[fn] = name
}

func (m *ModuleInfo) LookupFunctionReturn(fn *ast.FnDecl) (string, bool) {
	if m == nil || fn == nil {
		return "", false
	}
	name, ok := m.FunctionReturns[fn]
	return name, ok
}

type Expr interface {
	typeName() string
}

func ExprTypeName(expr Expr) string {
	if expr == nil {
		return ""
	}
	return expr.typeName()
}

type IntLit struct {
	Value int32
}

func (*IntLit) typeName() string { return "i32" }

type Ident struct {
	Symbol *symbols.Symbol
}

func (*Ident) typeName() string { return "i32" }

type Unary struct {
	Op  string
	Arg Expr
}

func (*Unary) typeName() string { return "i32" }

type Binary struct {
	Op    string
	Left  Expr
	Right Expr
}

func (*Binary) typeName() string { return "i32" }
