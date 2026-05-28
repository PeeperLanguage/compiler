package binding

import (
	"compiler/internal/analysis/semantics/symbols"
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

type ModuleInfo struct {
	Nodes           map[ast.Node]*Resolution
	FunctionSymbols map[*ast.FnDecl]*symbols.Symbol
	FunctionLocals  map[*ast.FnDecl][]*symbols.Symbol
}

func NewModuleInfo() *ModuleInfo {
	return &ModuleInfo{
		Nodes:           make(map[ast.Node]*Resolution),
		FunctionSymbols: make(map[*ast.FnDecl]*symbols.Symbol),
		FunctionLocals:  make(map[*ast.FnDecl][]*symbols.Symbol),
	}
}

func (m *ModuleInfo) BindNode(node ast.Node, resolution *Resolution) {
	if m == nil || node == nil || resolution == nil {
		return
	}
	m.Nodes[node] = resolution
}

func (m *ModuleInfo) LookupNode(node ast.Node) (*Resolution, bool) {
	if m == nil || node == nil {
		return nil, false
	}
	resolution, found := m.Nodes[node]
	return resolution, found
}

func (m *ModuleInfo) BindFunctionSymbol(fn *ast.FnDecl, sym *symbols.Symbol) {
	if m == nil || fn == nil || sym == nil {
		return
	}
	m.FunctionSymbols[fn] = sym
}

func (m *ModuleInfo) AddFunctionLocal(fn *ast.FnDecl, sym *symbols.Symbol) {
	if m == nil || fn == nil || sym == nil {
		return
	}
	m.FunctionLocals[fn] = append(m.FunctionLocals[fn], sym)
}
