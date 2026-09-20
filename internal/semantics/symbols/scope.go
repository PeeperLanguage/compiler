package symbols

import (
	"errors"
	"fmt"
)

type Scope struct {
	parent *Scope
	byName map[string]*Symbol
	order  []*Symbol
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
		if _, exists := s.byName[sym.Name]; exists {
			return fmt.Errorf("`%s` already exists in this scope", sym.Name)
		}
		s.byName[sym.Name] = sym
	}
	s.order = append(s.order, sym)
	return nil
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
