// Package bindingresult defines one staged symbol/scope graph completed by collection, binding, resolution, and typechecking.
package bindingresult

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
)

type Result struct {
	BlockScopes        map[ast.NodeID]*symbols.Scope
	NodeSymbols        map[ast.NodeID]*symbols.Symbol
	MethodsByReceiver  map[string][]*symbols.Symbol
	MethodsByDecl      map[ast.NodeID]*symbols.Symbol
	OperationFunctions []*symbols.Symbol
}

func New() *Result {
	return &Result{
		BlockScopes:        make(map[ast.NodeID]*symbols.Scope),
		NodeSymbols:        make(map[ast.NodeID]*symbols.Symbol),
		MethodsByReceiver:  make(map[string][]*symbols.Symbol),
		MethodsByDecl:      make(map[ast.NodeID]*symbols.Symbol),
		OperationFunctions: make([]*symbols.Symbol, 0),
	}
}
