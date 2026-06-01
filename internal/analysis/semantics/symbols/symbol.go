package symbols

import (
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"compiler/core/source"
	"compiler/internal/frontend/ast"
)

type SymbolID uint64

var nextSymbolID uint64

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
	Text() string
}

type Symbol struct {
	ID           SymbolID
	Name         string
	Kind         Kind
	Type         Type
	IsPub        bool
	Initializing bool
	Location     *source.Location
	ASTNode      ast.Node
	Scope        interface{} // Pointer to table.Scope (only if Kind == SymbolFunc)
}

func New(name string, kind Kind, node ast.Node) *Symbol {
	loc := &source.Location{}
	if node != nil {
		loc = node.Loc()
	}
	return &Symbol{
		ID:       SymbolID(atomic.AddUint64(&nextSymbolID, 1)),
		Name:     name,
		Kind:     kind,
		IsPub:    IsPubName(name),
		Location: loc,
		ASTNode:  node,
	}
}

func IsPubName(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}
