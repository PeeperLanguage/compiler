package typeinfo

import (
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/frontend/ast"
)

type ModuleInfo struct {
	Externs   []declinfo.ExternDecl
	Functions []*Function
}

type Function struct {
	Symbol   *symbols.Symbol
	Decl     *ast.FnDecl
	Scope    *table.Scope
	Bindings []Binding
	Returns  []Return
}

type Binding struct {
	Symbol *symbols.Symbol
	Value  Expr
}

type Return struct {
	Stmt  *ast.ReturnStmt
	Value Expr
}

type Expr interface {
	typeName() string
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
