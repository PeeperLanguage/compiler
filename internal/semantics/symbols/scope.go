package symbols

import (
	"errors"
	"fmt"

	"compiler/internal/frontend/ast"
)

type Scope struct {
	parent *Scope
	byName map[string]SymbolID
	byID   map[SymbolID]*Symbol
	order  []SymbolID
}

func NewScope(parent *Scope) *Scope {
	return &Scope{
		parent: parent,
		byName: make(map[string]SymbolID),
		byID:   make(map[SymbolID]*Symbol),
		order:  make([]SymbolID, 0),
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
		s.byName[sym.Name] = sym.ID
	}
	s.byID[sym.ID] = sym
	s.order = append(s.order, sym.ID)
	return nil
}

func (s *Scope) LookupLocal(name string) (*Symbol, bool) {
	if s == nil {
		return nil, false
	}
	id, ok := s.byName[name]
	if !ok {
		return nil, false
	}
	sym := s.byID[id]
	return sym, sym != nil
}

func (s *Scope) Lookup(name string) (*Symbol, bool) {
	for scope := s; scope != nil; scope = scope.parent {
		if id, ok := scope.byName[name]; ok {
			sym := scope.byID[id]
			if sym != nil {
				return sym, true
			}
			return nil, false
		}
	}
	return nil, false
}

func (s *Scope) LookupNode(node ast.Node) (*Symbol, bool) {
	if s == nil || node == nil {
		return nil, false
	}
	for _, id := range s.order {
		sym := s.byID[id]
		if sym != nil && sym.ASTNode == node {
			return sym, true
		}
	}
	return nil, false
}

func (s *Scope) Symbols() []*Symbol {
	if s == nil {
		return nil
	}
	out := make([]*Symbol, 0, len(s.order))
	for _, id := range s.order {
		if sym := s.byID[id]; sym != nil {
			out = append(out, sym)
		}
	}
	return out
}

func (s *Scope) IsMutableBinding(name string) bool {
	sym, found := s.Lookup(name)
	return found && sym != nil && (sym.Kind == SymbolVar || sym.Kind == SymbolParam) && sym.IsMutable()
}
