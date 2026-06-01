package declinfo

import (
	"compiler/core/source"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/frontend/ast"
)

type Function struct {
	Symbol      *symbols.Symbol
	Decl        *ast.FnDecl
	Scope       *table.Scope
	BlockScopes map[*ast.BlockStmt]*table.Scope
	LocalDecls  []LocalDecl
	LocalNames  map[string][]LocalDecl
}

type ExternDecl struct {
	Symbol *symbols.Symbol
	Decl   *ast.FnDecl
}

type LocalDecl struct {
	Name string
	Loc  *source.Location
}
