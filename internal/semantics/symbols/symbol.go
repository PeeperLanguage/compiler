package symbols

import (
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"compiler/internal/frontend/ast"
	"compiler/internal/source"
)

type SymbolID uint64

var nextSymbolID atomic.Uint64

type Kind string

const (
	SymbolImport  Kind = "import"
	SymbolVar     Kind = "var"
	SymbolConst   Kind = "const"
	SymbolType    Kind = "type"
	SymbolFunc    Kind = "func"
	SymbolMethod  Kind = "method"
	SymbolParam   Kind = "param"
	SymbolField   Kind = "field"
	SymbolStatic  Kind = "static"
	SymbolVariant Kind = "variant"
	SymbolError   Kind = "error_member"
	SymbolUnknown Kind = "unknown"
)

type Type interface {
	TypeNode()
	Text() string
}

type Symbol struct {
	ID           SymbolID
	Name         string
	Kind         Kind
	Type         Type
	IsPub        bool
	Initializing bool
	Used         bool
	Location     *source.Location
	ASTNode      ast.Node
	Scope        any // Pointer to table.Scope (only if Kind == SymbolFunc)
}

func New(name string, kind Kind, node ast.Node) *Symbol {
	return &Symbol{
		ID:       SymbolID(nextSymbolID.Add(1)),
		Name:     name,
		Kind:     kind,
		IsPub:    IsPubName(name),
		Location: ast.LocOf(node),
		ASTNode:  node,
	}
}

func (s *Symbol) BindType(typ Type) bool {
	if s == nil || typ == nil {
		return false
	}
	s.Type = typ
	return true
}

// SymbolType returns the semantic type stored on sym, or (nil, false) if sym
// carries no type.
// This is the canonical single-source-of-truth lookup shared across all passes.
func GetSymbolType(sym *Symbol) (Type, bool) {
	if sym == nil || sym.Type == nil {
		return nil, false
	}
	return sym.Type, true
}

func (s *Symbol) IsMutable() bool {
	if s == nil {
		return false
	}
	decl, ok := s.ASTNode.(*ast.LetDecl)
	return ok && decl != nil && decl.IsMutable
}

func IsPubName(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}
