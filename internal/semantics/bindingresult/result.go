// Package bindingresult defines one staged symbol/scope graph completed by collection, binding, resolution, and typechecking.
package bindingresult

import (
	"cmp"
	"slices"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type Result struct {
	blockScopes        map[ast.NodeID]*symbols.Scope
	nodeSymbols        map[ast.NodeID]*symbols.Symbol
	methodsByReceiver  map[string][]*symbols.Symbol
	operationFunctions []*symbols.Symbol
}

func New() *Result {
	return &Result{
		blockScopes:        make(map[ast.NodeID]*symbols.Scope),
		nodeSymbols:        make(map[ast.NodeID]*symbols.Symbol),
		methodsByReceiver:  make(map[string][]*symbols.Symbol),
		operationFunctions: make([]*symbols.Symbol, 0),
	}
}

// Bind records which declaration a syntax occurrence denotes. The caller owns
// name resolution; this result owns the occurrence index used by later phases.
func (r *Result) Bind(node ast.Node, sym *symbols.Symbol) {
	if r == nil || node == nil || sym == nil {
		return
	}
	r.nodeSymbols[node.ID()] = sym
}

// BindID is used for generated syntax whose stable node identity is already in
// hand. Source code should prefer Bind so the key choice stays local here.
func (r *Result) BindID(id ast.NodeID, sym *symbols.Symbol) {
	if r == nil || id == 0 || sym == nil {
		return
	}
	r.nodeSymbols[id] = sym
}

// Unbind removes stale syntax identity during invalidation or evidence-repair
// tests. Normal semantic production only adds bindings within a generation.
func (r *Result) Unbind(node ast.Node) {
	if r == nil || node == nil {
		return
	}
	delete(r.nodeSymbols, node.ID())
}

func (r *Result) Symbol(node ast.Node) *symbols.Symbol {
	if r == nil || node == nil {
		return nil
	}
	return r.nodeSymbols[node.ID()]
}

func (r *Result) SymbolID(id ast.NodeID) *symbols.Symbol {
	if r == nil || id == 0 {
		return nil
	}
	return r.nodeSymbols[id]
}

func (r *Result) SetScope(node ast.Node, scope *symbols.Scope) {
	if r == nil || node == nil || scope == nil {
		return
	}
	r.blockScopes[node.ID()] = scope
}

func (r *Result) SetScopeID(id ast.NodeID, scope *symbols.Scope) {
	if r == nil || id == 0 || scope == nil {
		return
	}
	r.blockScopes[id] = scope
}

func (r *Result) Scope(node ast.Node) *symbols.Scope {
	if r == nil || node == nil {
		return nil
	}
	return r.blockScopes[node.ID()]
}

func (r *Result) ScopeID(id ast.NodeID) *symbols.Scope {
	if r == nil || id == 0 {
		return nil
	}
	return r.blockScopes[id]
}

func (r *Result) ForEachScope(fn func(*symbols.Scope)) {
	if r == nil || fn == nil {
		return
	}
	for _, scope := range r.blockScopes {
		if scope != nil {
			fn(scope)
		}
	}
}

func (r *Result) ForEachSymbol(fn func(*symbols.Symbol)) {
	if r == nil || fn == nil {
		return
	}
	for _, sym := range r.nodeSymbols {
		if sym != nil {
			fn(sym)
		}
	}
}

// RegisterMethod publishes one resolved receiver method. Method ownership is
// keyed by semantic declaration identity, never display text. It returns the
// existing same-name method when registration would be a redeclaration.
func (r *Result) RegisterMethod(receiver typeinfo.Type, method *symbols.Symbol) *symbols.Symbol {
	if r == nil || method == nil {
		return nil
	}
	key, ok := typeinfo.ReceiverIdentity(receiver)
	if !ok {
		return nil
	}
	for _, existing := range r.methodsByReceiver[key] {
		if existing != nil && existing.Name == method.Name {
			return existing
		}
	}
	r.methodsByReceiver[key] = append(r.methodsByReceiver[key], method)
	return nil
}

func (r *Result) Methods(receiver typeinfo.Type) []*symbols.Symbol {
	if r == nil {
		return nil
	}
	key, ok := typeinfo.ReceiverIdentity(receiver)
	if !ok {
		return nil
	}
	return r.methodsByReceiver[key]
}

func (r *Result) ForEachMethod(fn func(receiverIdentity string, method *symbols.Symbol)) {
	if r == nil || fn == nil {
		return
	}
	for receiver, methods := range r.methodsByReceiver {
		for _, method := range methods {
			if method != nil {
				fn(receiver, method)
			}
		}
	}
}

func (r *Result) AddOperationFunction(sym *symbols.Symbol) {
	if r == nil || sym == nil {
		return
	}
	r.operationFunctions = append(r.operationFunctions, sym)
}

func (r *Result) SortOperationFunctions() {
	if r == nil {
		return
	}
	slices.SortFunc(r.operationFunctions, func(left, right *symbols.Symbol) int {
		return cmp.Compare(left.Name, right.Name)
	})
}

func (r *Result) OperationFunctions() []*symbols.Symbol {
	if r == nil {
		return nil
	}
	return r.operationFunctions
}
