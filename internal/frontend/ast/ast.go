// Package ast is the syntax tree of one file, stored flat: every node is a
// value in one array and refers to its children by index. A tree takes a few
// allocations, it holds no pointers for the garbage collector to scan, and
// later phases keep their results in slices indexed by node.
//
// Each kind's comment says what its A and B slots hold: a node index, None, or
// an offset into Extra. The parser writes the slots and the accessors read them;
// keep the three in step when a kind changes.
//
// Nodes are stored in the order the parser makes them, and a node is made when
// the parser has read up to its end, so node ends never decrease along Nodes.
// NodeAt relies on this.
package ast

import (
	"iter"
	"sort"

	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

// Index is the position of a node in Tree.Nodes.
type Index int32

// None is an absent child. Nodes[0] is a placeholder, so no real node has index 0.
const None Index = 0

// EmptyList is the Extra offset every empty list shares: Extra[0] is 0, a
// list of no items.
const EmptyList Index = 0

type Kind uint8

const (
	KindNone                Kind = iota // the placeholder at index 0
	KindModule                          // A: list of imports, B: list of declarations
	KindImportDecl                      // A: path (StringLiteral), B: alias (DeclaredName) or None
	KindFunctionDecl                    // A: name (DeclaredName), B: Extra offset of [receiver (Parameter) or None, list of type parameters (DeclaredName), list of parameters, result type or None, list of return origins (NameReference), body (Block) or None, metadata or None]; an interface method has no body and no metadata
	KindConstDecl                       // A: name (DeclaredName), B: Extra offset of [type or None, value or None, metadata or None]
	KindStructDecl                      // A: name (DeclaredName), B: Extra offset of [list of type parameters (DeclaredName), StructType, metadata or None]
	KindEnumDecl                        // A: name (DeclaredName), B: Extra offset of [list of type parameters (DeclaredName), EnumType, metadata or None]
	KindInterfaceDecl                   // A: name (DeclaredName), B: Extra offset of [list of type parameters (DeclaredName), InterfaceType, metadata or None]
	KindTypeAliasDecl                   // A: name (DeclaredName), B: Extra offset of [list of type parameters (DeclaredName), type, metadata or None]
	KindMetadata                        // A: DocComment or None, B: list of attributes; what comes before a declaration
	KindDocComment                      // the `///` lines before a declaration; its text is the node's source text
	KindAttribute                       // A: name (NameReference), B: list of arguments, as in `#[extern("name")]`
	KindParameter                       // Op: MUT or 0, A: name (DeclaredName) or None, B: Extra offset of [type, default value or None]
	KindDeclaredName                    // the name a declaration introduces; its text is the node's source text
	KindTypeName                        // a type written as a name; its text is the node's source text
	KindPath                            // A: list of segments (TypeName or GenericType), as in `a::B<T>`
	KindGenericType                     // A: name (TypeName), B: list of type arguments, as in `Box<T>`
	KindReferenceType                   // Op: MUT or 0, A: target type, as in `&T` or `&mut T`
	KindOptionalType                    // A: inner type, as in `?T`
	KindOwnedPointerType                // A: target type, as in `*T`
	KindRawPointerType                  // the type `rawptr`
	KindFixedArrayType                  // A: length (NumberLiteral), B: element type, as in `[4]T`
	KindDynamicArrayType                // A: element type, as in `[]T`
	KindSliceType                       // A: element type, as in `[..]T`
	KindFunctionType                    // A: list of parameters, B: Extra offset of [result type or None, list of return origins (NameReference)]
	KindStructType                      // A: list of fields
	KindField                           // A: name (DeclaredName), B: type
	KindEnumType                        // A: list of variants
	KindVariant                         // A: name (DeclaredName), B: payload type or None
	KindInterfaceType                   // A: list of methods (FunctionDecl)
	KindInvalidType                     // a type that could not be parsed
	KindBlock                           // A: list of statements
	KindLetStatement                    // A: name (DeclaredName), B: Extra offset of [type or None, value]
	KindReturnStatement                 // A: value or None
	KindExpressionStatement             // A: expression
	KindNameReference                   // a use of a declared name; its text is the node's source text
	KindNumberLiteral                   // the number is the node's source text
	KindStringLiteral                   // the literal is the node's source text; literal.Value decodes it
	KindBinaryExpression                // Op: operator, A: left, B: right
	KindCallExpression                  // A: callee, B: list of arguments
	KindParentheses                     // A: the expression inside `( )`
	KindInvalidExpression               // an expression that could not be parsed
)

var kindNames = [...]string{
	KindNone:                "none",
	KindModule:              "module",
	KindImportDecl:          "import declaration",
	KindFunctionDecl:        "function declaration",
	KindConstDecl:           "const declaration",
	KindStructDecl:          "struct declaration",
	KindEnumDecl:            "enum declaration",
	KindInterfaceDecl:       "interface declaration",
	KindTypeAliasDecl:       "type alias declaration",
	KindMetadata:            "metadata",
	KindDocComment:          "doc comment",
	KindAttribute:           "attribute",
	KindParameter:           "parameter",
	KindDeclaredName:        "declared name",
	KindTypeName:            "type name",
	KindPath:                "path",
	KindGenericType:         "generic type",
	KindReferenceType:       "reference type",
	KindOptionalType:        "optional type",
	KindOwnedPointerType:    "owned pointer type",
	KindRawPointerType:      "raw pointer type",
	KindFixedArrayType:      "fixed array type",
	KindDynamicArrayType:    "dynamic array type",
	KindSliceType:           "slice type",
	KindFunctionType:        "function type",
	KindStructType:          "struct type",
	KindField:               "field",
	KindEnumType:            "enum type",
	KindVariant:             "variant",
	KindInterfaceType:       "interface type",
	KindInvalidType:         "invalid type",
	KindBlock:               "block",
	KindLetStatement:        "let statement",
	KindReturnStatement:     "return statement",
	KindExpressionStatement: "expression statement",
	KindNameReference:       "name reference",
	KindNumberLiteral:       "number literal",
	KindStringLiteral:       "string literal",
	KindBinaryExpression:    "binary expression",
	KindCallExpression:      "call expression",
	KindParentheses:         "parentheses",
	KindInvalidExpression:   "invalid expression",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

type Node struct {
	Kind Kind
	Op   token.Kind
	Span source.Span
	A, B Index
}

// Tree is the syntax tree of one file. It never changes once the parser
// returns it.
type Tree struct {
	File  *source.File
	Nodes []Node
	Extra []Index // lists, each its length followed by its items, and extra children; Extra[0] is the empty list
	Root  Index   // the module
}

// List returns the items of the list stored at Extra offset at.
func (t *Tree) List(at Index) []Index { return t.Extra[at+1 : at+1+t.Extra[at]] }

// Text returns the source text of node i.
func (t *Tree) Text(i Index) string {
	s := t.Nodes[i].Span
	return t.File.Text[s.Start:s.End]
}

// FunctionDecl is a function declaration's children; TypeParameters,
// Parameters and Origins are lists' Extra offsets.
type FunctionDecl struct {
	Name, Receiver, TypeParameters, Parameters, Result, Origins, Body, Metadata Index
}

func (t *Tree) FunctionDecl(i Index) FunctionDecl {
	n := t.Nodes[i]
	x := t.Extra[n.B : n.B+7]
	return FunctionDecl{Name: n.A, Receiver: x[0], TypeParameters: x[1], Parameters: x[2], Result: x[3], Origins: x[4], Body: x[5], Metadata: x[6]}
}

// ConstDecl is a const declaration's children.
type ConstDecl struct{ Name, Type, Value, Metadata Index }

func (t *Tree) ConstDecl(i Index) ConstDecl {
	n := t.Nodes[i]
	x := t.Extra[n.B : n.B+3]
	return ConstDecl{Name: n.A, Type: x[0], Value: x[1], Metadata: x[2]}
}

// TypeDecl is the children of a struct, enum, interface or type alias
// declaration; TypeParameters is a list's Extra offset.
type TypeDecl struct{ Name, TypeParameters, Type, Metadata Index }

func (t *Tree) TypeDecl(i Index) TypeDecl {
	n := t.Nodes[i]
	x := t.Extra[n.B : n.B+3]
	return TypeDecl{Name: n.A, TypeParameters: x[0], Type: x[1], Metadata: x[2]}
}

// Parameter is a parameter's children.
type Parameter struct{ Name, Type, Default Index }

func (t *Tree) Parameter(i Index) Parameter {
	n := t.Nodes[i]
	return Parameter{Name: n.A, Type: t.Extra[n.B], Default: t.Extra[n.B+1]}
}

// FunctionType is a function type's children; Parameters and Origins are
// lists' Extra offsets.
type FunctionType struct{ Parameters, Result, Origins Index }

func (t *Tree) FunctionType(i Index) FunctionType {
	n := t.Nodes[i]
	return FunctionType{Parameters: n.A, Result: t.Extra[n.B], Origins: t.Extra[n.B+1]}
}

// LetStatement is a let statement's children.
type LetStatement struct{ Name, Type, Value Index }

func (t *Tree) LetStatement(i Index) LetStatement {
	n := t.Nodes[i]
	return LetStatement{Name: n.A, Type: t.Extra[n.B], Value: t.Extra[n.B+1]}
}

// Children returns the children of node i in source order, the items of its
// lists in place. It is the one place that walks every kind's slots, so a walk
// over the tree needs no copy of the slot layout.
func (t *Tree) Children(i Index) iter.Seq[Index] {
	return func(yield func(Index) bool) {
		stopped := false
		node := func(child Index) {
			if !stopped && child != None {
				stopped = !yield(child)
			}
		}
		list := func(at Index) {
			for _, child := range t.List(at) {
				node(child)
			}
		}
		n := t.Nodes[i]
		switch n.Kind {
		case KindModule:
			list(n.A)
			list(n.B)
		case KindImportDecl, KindBinaryExpression, KindFixedArrayType, KindField, KindVariant:
			node(n.A)
			node(n.B)
		case KindFunctionDecl:
			f := t.FunctionDecl(i)
			node(f.Metadata)
			node(f.Receiver)
			node(f.Name)
			list(f.TypeParameters)
			list(f.Parameters)
			node(f.Result)
			list(f.Origins)
			node(f.Body)
		case KindConstDecl:
			c := t.ConstDecl(i)
			node(c.Metadata)
			node(c.Name)
			node(c.Type)
			node(c.Value)
		case KindStructDecl, KindEnumDecl, KindInterfaceDecl, KindTypeAliasDecl:
			d := t.TypeDecl(i)
			node(d.Metadata)
			node(d.Name)
			list(d.TypeParameters)
			node(d.Type)
		case KindMetadata, KindAttribute:
			node(n.A)
			list(n.B)
		case KindStructType, KindEnumType, KindInterfaceType:
			list(n.A)
		case KindParameter:
			p := t.Parameter(i)
			node(p.Name)
			node(p.Type)
			node(p.Default)
		case KindLetStatement:
			l := t.LetStatement(i)
			node(l.Name)
			node(l.Type)
			node(l.Value)
		case KindPath, KindBlock:
			list(n.A)
		case KindGenericType, KindCallExpression:
			node(n.A)
			list(n.B)
		case KindReferenceType, KindOptionalType, KindOwnedPointerType, KindDynamicArrayType, KindSliceType,
			KindReturnStatement, KindExpressionStatement, KindParentheses:
			node(n.A)
		case KindFunctionType:
			f := t.FunctionType(i)
			list(f.Parameters)
			node(f.Result)
			list(f.Origins)
		}
	}
}

// NodeAt returns the smallest node whose span holds offset, or None. Node ends
// never decrease along Nodes, so the nodes ending after offset are a suffix;
// the first of them that starts at or before offset holds it, and every node
// before that one in the suffix lies after offset.
func (t *Tree) NodeAt(offset int) Index {
	first := sort.Search(len(t.Nodes), func(i int) bool { return t.Nodes[i].Span.End > offset })
	for i := first; i < len(t.Nodes); i++ {
		if t.Nodes[i].Span.Start <= offset {
			return Index(i)
		}
	}
	return None
}
