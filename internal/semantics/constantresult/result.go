// Package constantresult defines constant-evaluation artifacts for one semantic generation.
package constantresult

import (
	"compiler/internal/constvalue"
	"compiler/internal/semantics/symbols"
)

// Result separates authoritative module constants from mutable lazy query entries.
type Result struct {
	ModuleValues map[symbols.SymbolID]constvalue.Value
	QueryCache   map[symbols.SymbolID]constvalue.Value
}

func New() *Result {
	return &Result{
		ModuleValues: make(map[symbols.SymbolID]constvalue.Value),
		QueryCache:   make(map[symbols.SymbolID]constvalue.Value),
	}
}
