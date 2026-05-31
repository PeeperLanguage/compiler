package declinfo

import (
	"compiler/core/source"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/frontend/ast"
)

type ResolutionKind string

const (
	ResolutionSymbol ResolutionKind = "symbol"
)

type Resolution struct {
	Kind   ResolutionKind
	Symbol *symbols.Symbol
}

type Function struct {
	Symbol     *symbols.Symbol
	Decl       *ast.FnDecl
	Scope      *table.Scope
	LocalDecls []LocalDecl
	LocalNames map[string][]LocalDecl
}

type ExternDecl struct {
	Symbol *symbols.Symbol
	Decl   *ast.FnDecl
}

type LocalDecl struct {
	Name string
	Loc  *source.Location
}
