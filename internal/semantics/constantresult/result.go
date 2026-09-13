// Package constantresult defines constant-evaluation artifacts for one semantic generation.
package constantresult

import (
	"compiler/internal/constvalue"
	"compiler/internal/semantics/symbols"
)

// Result separates authoritative module constants from mutable lazy query entries.
// Storage stays private so producers and consumers express lifecycle operations
// instead of depending on backing-map layout.
type Result struct {
	moduleValues map[symbols.SymbolID]constvalue.Value
	queryCache   map[symbols.SymbolID]constvalue.Value
}

func New() *Result {
	return &Result{
		moduleValues: make(map[symbols.SymbolID]constvalue.Value),
		queryCache:   make(map[symbols.SymbolID]constvalue.Value),
	}
}

// ClearPublished removes authoritative module values before final publication.
func (r *Result) ClearPublished() {
	if r != nil {
		clear(r.moduleValues)
	}
}

// Publish records an authoritative module constant and removes any provisional
// query entry for the same declaration.
func (r *Result) Publish(id symbols.SymbolID, value constvalue.Value) {
	if r == nil || value == nil {
		return
	}
	r.moduleValues[id] = value
	delete(r.queryCache, id)
}

// Published returns the authoritative value for a module constant. Nil means no
// value has been published; constant evaluation never publishes nil.
func (r *Result) Published(id symbols.SymbolID) constvalue.Value {
	if r == nil {
		return nil
	}
	return r.moduleValues[id]
}

// Cached returns one provisional lazy-query value.
func (r *Result) Cached(id symbols.SymbolID) (constvalue.Value, bool) {
	if r == nil {
		return nil, false
	}
	value, ok := r.queryCache[id]
	return value, ok
}

// Cache records one provisional lazy-query value.
func (r *Result) Cache(id symbols.SymbolID, value constvalue.Value) {
	if r != nil && value != nil {
		r.queryCache[id] = value
	}
}

// DiscardCached removes a provisional value, typically before authoritative
// recomputation of a module constant.
func (r *Result) DiscardCached(id symbols.SymbolID) {
	if r != nil {
		delete(r.queryCache, id)
	}
}
