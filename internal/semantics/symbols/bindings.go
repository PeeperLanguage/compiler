package symbols

import (
	"cmp"
	"slices"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/typeinfo"
)

type Bindings struct {
	blockScopes        map[ast.NodeID]*Scope
	nodeSymbols        map[ast.NodeID]*Symbol
	methodsByReceiver  map[string][]*Symbol
	operationFunctions []*Symbol
}

func NewBindings() *Bindings {
	return &Bindings{
		blockScopes:        make(map[ast.NodeID]*Scope),
		nodeSymbols:        make(map[ast.NodeID]*Symbol),
		methodsByReceiver:  make(map[string][]*Symbol),
		operationFunctions: make([]*Symbol, 0),
	}
}

// Bind records which declaration a syntax occurrence denotes. The caller owns
// name resolution; bindings own the occurrence index used by later phases.
func (r *Bindings) Bind(node ast.Node, sym *Symbol) {
	if r == nil || node == nil || sym == nil {
		return
	}
	r.nodeSymbols[node.ID()] = sym
}

// BindID is used for generated syntax whose stable node identity is already in
// hand. Source code should prefer Bind so the key choice stays local here.
func (r *Bindings) BindID(id ast.NodeID, sym *Symbol) {
	if r == nil || id == 0 || sym == nil {
		return
	}
	r.nodeSymbols[id] = sym
}

func (r *Bindings) Symbol(node ast.Node) *Symbol {
	if r == nil || node == nil {
		return nil
	}
	return r.nodeSymbols[node.ID()]
}

func (r *Bindings) SymbolID(id ast.NodeID) *Symbol {
	if r == nil || id == 0 {
		return nil
	}
	return r.nodeSymbols[id]
}

func (r *Bindings) SetScope(node ast.Node, scope *Scope) {
	if r == nil || node == nil || scope == nil {
		return
	}
	r.blockScopes[node.ID()] = scope
}

func (r *Bindings) Scope(node ast.Node) *Scope {
	if r == nil || node == nil {
		return nil
	}
	return r.blockScopes[node.ID()]
}

func (r *Bindings) ScopeID(id ast.NodeID) *Scope {
	if r == nil || id == 0 {
		return nil
	}
	return r.blockScopes[id]
}

func (r *Bindings) ForEachScope(fn func(*Scope)) {
	if r == nil || fn == nil {
		return
	}
	for _, scope := range r.blockScopes {
		if scope != nil {
			fn(scope)
		}
	}
}

// RegisterMethod publishes one resolved receiver method. Method ownership is
// keyed by semantic declaration identity, never display text. It returns the
// existing same-name method when registration would be a redeclaration.
func (r *Bindings) RegisterMethod(receiver typeinfo.Type, method *Symbol) *Symbol {
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

func (r *Bindings) Methods(receiver typeinfo.Type) []*Symbol {
	if r == nil {
		return nil
	}
	key, ok := typeinfo.ReceiverIdentity(receiver)
	if !ok {
		return nil
	}
	return r.methodsByReceiver[key]
}

func (r *Bindings) ForEachMethod(fn func(receiverIdentity string, method *Symbol)) {
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

func (r *Bindings) AddOperationFunction(sym *Symbol) {
	if r == nil || sym == nil {
		return
	}
	r.operationFunctions = append(r.operationFunctions, sym)
}

func (r *Bindings) SortOperationFunctions() {
	if r == nil {
		return
	}
	slices.SortFunc(r.operationFunctions, func(left, right *Symbol) int {
		return cmp.Compare(left.Name, right.Name)
	})
}

func (r *Bindings) OperationFunctions() []*Symbol {
	if r == nil {
		return nil
	}
	return r.operationFunctions
}
