package symbols

import (
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"compiler/internal/frontend/ast"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

type SymbolID uint64

var nextSymbolID atomic.Uint64

type Kind string

type CompilerOp string

const (
	CompilerOpAlloc     CompilerOp = "alloc"
	CompilerOpAppend    CompilerOp = "append"
	CompilerOpReserve   CompilerOp = "reserve"
	CompilerOpResize    CompilerOp = "resize"
	CompilerOpShrink    CompilerOp = "shrink"
	CompilerOpLen       CompilerOp = "len"
	CompilerOpAsBytes   CompilerOp = "as_bytes"
	CompilerOpAsChars   CompilerOp = "as_chars"
	CompilerOpFromBytes CompilerOp = "from_bytes"
)

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

type Symbol struct {
	ID              SymbolID
	Name            string
	Kind            Kind
	Type            typeinfo.Type
	IsPub           bool
	Mutable         bool
	IsReceiver      bool
	used            bool
	requiresMutable bool
	CompilerOp      CompilerOp
	DefiningModule  moduleid.ID
	Location        *source.Location
	MutableLocation *source.Location
	ASTNode         ast.Node
	Scope           *Scope
}

func New(name string, kind Kind, node ast.Node, location *source.Location) *Symbol {
	return &Symbol{
		ID:       SymbolID(nextSymbolID.Add(1)),
		Name:     name,
		Kind:     kind,
		IsPub:    IsPubName(name),
		Location: location,
		ASTNode:  node,
	}
}

func (s *Symbol) BindType(typ typeinfo.Type) {
	if s == nil || typ == nil {
		return
	}
	s.Type = typ
}

// GetSymbolType returns semantic type stored on sym, or (nil, false) when sym
// is nil or carries no type. It is a nil-safe accessor; phase ownership remains
// with the code that publishes and consumes each symbol type.
func GetSymbolType(sym *Symbol) (typeinfo.Type, bool) {
	if sym == nil || sym.Type == nil {
		return nil, false
	}
	return sym.Type, true
}

func (s *Symbol) MarkUsed() {
	if s != nil {
		s.used = true
	}
}

func (s *Symbol) IsUsed() bool {
	return s != nil && s.used
}

func (s *Symbol) RequireMutable() {
	if s != nil {
		s.requiresMutable = true
	}
}

func (s *Symbol) RequiresMutable() bool {
	return s != nil && s.requiresMutable
}

func (s *Symbol) IsMutable() bool {
	if s == nil {
		return false
	}
	if s.Kind == SymbolParam {
		return s.Mutable
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
