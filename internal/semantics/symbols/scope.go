package symbols

import (
	"errors"
	"fmt"
)

type Scope struct {
	parent *Scope
	// continuesParent marks the scope of the rest of a block (after a guard
	// that leaves): its names and the parent's are the names of one block.
	continuesParent bool
	byName          map[string]*Symbol
	order           []*Symbol
}

func NewScope(parent *Scope) *Scope {
	return &Scope{
		parent: parent,
		byName: make(map[string]*Symbol),
		order:  make([]*Symbol, 0),
	}
}

func (s *Scope) Parent() *Scope {
	if s == nil {
		return nil
	}
	return s.parent
}

func (s *Scope) Declare(sym *Symbol) error {
	if s == nil || sym == nil {
		return errors.New("invalid symbol or scope")
	}
	if sym.Name != "_" {
		if _, exists := s.Declared(sym.Name); exists {
			return fmt.Errorf("`%s` already exists in this scope", sym.Name)
		}
		s.byName[sym.Name] = sym
	}
	s.order = append(s.order, sym)
	return nil
}

// ContinueParent makes s the rest of its parent's block: from here on a name
// of that block cannot be declared again in s.
func (s *Scope) ContinueParent() { s.continuesParent = true }

// Declared finds name among the names of the block s belongs to: those of s,
// and of the scopes it continues.
func (s *Scope) Declared(name string) (*Symbol, bool) {
	for block := s; block != nil; block = block.parent {
		if sym, ok := block.byName[name]; ok && sym != nil {
			return sym, true
		}
		if !block.continuesParent {
			break
		}
	}
	return nil, false
}

func (s *Scope) LookupLocal(name string) (*Symbol, bool) {
	if s == nil {
		return nil, false
	}
	sym, ok := s.byName[name]
	return sym, ok && sym != nil
}

func (s *Scope) Lookup(name string) (*Symbol, bool) {
	for scope := s; scope != nil; scope = scope.parent {
		if sym, ok := scope.byName[name]; ok {
			return sym, sym != nil
		}
	}
	return nil, false
}

func (s *Scope) Symbols() []*Symbol {
	if s == nil {
		return nil
	}
	return append([]*Symbol(nil), s.order...)
}

func (s *Scope) IsMutableBinding(name string) bool {
	sym, found := s.Lookup(name)
	return found && sym != nil && (sym.Kind == SymbolVar || sym.Kind == SymbolParam) && sym.IsMutable()
}
