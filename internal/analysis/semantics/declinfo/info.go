package declinfo

import (
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/frontend/ast"
)

type ModuleInfo struct {
	Externs   []ExternDecl
	Functions []*Function
}

type Function struct {
	Symbol *symbols.Symbol
	Decl   *ast.FnDecl
	Scope  *table.Scope
}

type ExternDecl struct {
	Symbol *symbols.Symbol
	Decl   *ast.FnDecl
}
