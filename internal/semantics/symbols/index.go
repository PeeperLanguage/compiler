package symbols

import (
	"cmp"
	"slices"

	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

type Index struct {
	blockScopes            map[source.NodeID]*Scope
	nodeSymbols            map[source.NodeID]*Symbol
	methodsByReceiver      map[string][]*Symbol
	operationFunctions     []*Symbol
	usedSymbols            map[SymbolID]struct{}
	mutableRequiredSymbols map[SymbolID]struct{}
	constants              map[SymbolID]constvalue.Value
}

func NewIndex() *Index {
	return &Index{
		blockScopes:            make(map[source.NodeID]*Scope),
		nodeSymbols:            make(map[source.NodeID]*Symbol),
		methodsByReceiver:      make(map[string][]*Symbol),
		operationFunctions:     make([]*Symbol, 0),
		usedSymbols:            make(map[SymbolID]struct{}),
		mutableRequiredSymbols: make(map[SymbolID]struct{}),
		constants:              make(map[SymbolID]constvalue.Value),
	}
}

// Bind records which declaration a syntax occurrence denotes. The caller owns
// name resolution; the index owns the occurrence lookup used by later phases.
func (r *Index) Bind(node ast.Node, sym *Symbol) {
	if r == nil || node == nil || sym == nil {
		return
	}
	r.nodeSymbols[node.ID()] = sym
}

// BindID is used for generated syntax whose stable node identity is already in
// hand. Source code should prefer Bind so the key choice stays local here.
func (r *Index) BindID(id source.NodeID, sym *Symbol) {
	if r == nil || !id.IsValid() || sym == nil {
		return
	}
	r.nodeSymbols[id] = sym
}

func (r *Index) Symbol(node ast.Node) *Symbol {
	if r == nil || node == nil {
		return nil
	}
	return r.nodeSymbols[node.ID()]
}

func (r *Index) SymbolID(id source.NodeID) *Symbol {
	if r == nil || !id.IsValid() {
		return nil
	}
	return r.nodeSymbols[id]
}

func (r *Index) SetScope(node ast.Node, scope *Scope) {
	if r == nil || node == nil || scope == nil {
		return
	}
	r.blockScopes[node.ID()] = scope
}

func (r *Index) Scope(node ast.Node) *Scope {
	if r == nil || node == nil {
		return nil
	}
	return r.blockScopes[node.ID()]
}

func (r *Index) ScopeID(id source.NodeID) *Scope {
	if r == nil || !id.IsValid() {
		return nil
	}
	return r.blockScopes[id]
}

func (r *Index) ForEachScope(fn func(*Scope)) {
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
func (r *Index) RegisterMethod(receiver typeinfo.Type, method *Symbol) *Symbol {
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

func (r *Index) Methods(receiver typeinfo.Type) []*Symbol {
	if r == nil {
		return nil
	}
	key, ok := typeinfo.ReceiverIdentity(receiver)
	if !ok {
		return nil
	}
	return r.methodsByReceiver[key]
}

func (r *Index) ForEachMethod(fn func(receiverIdentity string, method *Symbol)) {
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

func (r *Index) AddOperationFunction(sym *Symbol) {
	if r == nil || sym == nil {
		return
	}
	r.operationFunctions = append(r.operationFunctions, sym)
}

func (r *Index) SortOperationFunctions() {
	if r == nil {
		return
	}
	slices.SortFunc(r.operationFunctions, func(left, right *Symbol) int {
		return cmp.Compare(left.Name, right.Name)
	})
}

func (r *Index) OperationFunctions() []*Symbol {
	if r == nil {
		return nil
	}
	return r.operationFunctions
}

func (r *Index) MarkUsed(sym *Symbol) {
	if r == nil || sym == nil || !sym.ID.IsValid() {
		return
	}
	r.usedSymbols[sym.ID] = struct{}{}
}

func (r *Index) IsUsed(sym *Symbol) bool {
	if r == nil || sym == nil {
		return false
	}
	_, used := r.usedSymbols[sym.ID]
	return used
}

func (r *Index) ForEachUsedSymbolID(fn func(SymbolID)) {
	if r == nil || fn == nil {
		return
	}
	for id := range r.usedSymbols {
		fn(id)
	}
}

func (r *Index) RequireMutable(sym *Symbol) {
	if r == nil || sym == nil || !sym.ID.IsValid() {
		return
	}
	r.mutableRequiredSymbols[sym.ID] = struct{}{}
}

func (r *Index) RequiresMutable(sym *Symbol) bool {
	if r == nil || sym == nil {
		return false
	}
	_, required := r.mutableRequiredSymbols[sym.ID]
	return required
}

// PublishConstant records the authoritative value of a constant declaration for
// the current semantic generation. Provisional evaluator cache entries never
// enter the symbol index.
func (r *Index) PublishConstant(id SymbolID, value constvalue.Value) {
	if r == nil || !id.IsValid() || value == nil {
		return
	}
	if r.constants == nil {
		r.constants = make(map[SymbolID]constvalue.Value)
	}
	r.constants[id] = value
}

// ConstantValue returns the authoritative constant value for this generation.
func (r *Index) ConstantValue(id SymbolID) constvalue.Value {
	if r == nil || !id.IsValid() {
		return nil
	}
	return r.constants[id]
}

// ClearConstants removes all authoritative constant values for this generation.
func (r *Index) ClearConstants() {
	if r != nil {
		clear(r.constants)
	}
}
