package symbols

import (
	"reflect"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"compiler/core/source"
	"compiler/internal/frontend/ast"
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
	Location     *source.Location
	ASTNode      ast.Node
	Scope        interface{} // Pointer to table.Scope (only if Kind == SymbolFunc)
}

func New(name string, kind Kind, node ast.Node) *Symbol {
	var loc *source.Location
	if !isNilNode(node) {
		loc = node.Loc()
	}
	return &Symbol{
		ID:       SymbolID(nextSymbolID.Add(1)),
		Name:     name,
		Kind:     kind,
		IsPub:    IsPubName(name),
		Location: loc,
		ASTNode:  node,
	}
}

func isNilNode(node ast.Node) bool {
	if node == nil {
		return true
	}
	v := reflect.ValueOf(node)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
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

func IsPubName(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}
