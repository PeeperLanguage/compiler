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
	"sort"

	"compiler/internal/frontend/token"
	"compiler/internal/source"
)

// Index is the position of a node in Tree.Nodes.
type Index int32

// None is an absent child. Nodes[0] is a placeholder, so no real node has index 0.
const None Index = 0

type Kind uint8

const (
	KindNone                Kind = iota // the placeholder at index 0
	KindModule                          // A: list of imports, B: list of functions
	KindImportDecl                      // A: path (StringLiteral), B: alias (DeclaredName) or None
	KindFunctionDecl                    // A: name (DeclaredName), B: Extra offset of [list of parameters, result type or None, body (Block)]
	KindParameter                       // A: name (DeclaredName), B: type
	KindDeclaredName                    // the name a declaration introduces; its text is the node's source text
	KindTypeName                        // a type written as a name; its text is the node's source text
	KindBlock                           // A: list of statements
	KindLetStatement                    // A: name (DeclaredName), B: Extra offset of [type or None, value]
	KindReturnStatement                 // A: value or None
	KindExpressionStatement             // A: expression
	KindNameReference                   // a use of a declared name; its text is the node's source text
	KindNumberLiteral                   // the number is the node's source text
	KindStringLiteral                   // the literal is the node's source text; lexer.Value decodes it
	KindBinaryExpression                // Op: operator, A: left, B: right
	KindCallExpression                  // A: callee, B: list of arguments
	KindInvalidExpression               // an expression that could not be parsed
)

var kindNames = [...]string{
	KindNone:                "none",
	KindModule:              "module",
	KindImportDecl:          "import declaration",
	KindFunctionDecl:        "function declaration",
	KindParameter:           "parameter",
	KindDeclaredName:        "declared name",
	KindTypeName:            "type name",
	KindBlock:               "block",
	KindLetStatement:        "let statement",
	KindReturnStatement:     "return statement",
	KindExpressionStatement: "expression statement",
	KindNameReference:       "name reference",
	KindNumberLiteral:       "number literal",
	KindStringLiteral:       "string literal",
	KindBinaryExpression:    "binary expression",
	KindCallExpression:      "call expression",
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
	Extra []Index // lists, each its length followed by its items, and extra children
	Root  Index   // the module
}

// List returns the items of the list stored at Extra offset at.
func (t *Tree) List(at Index) []Index { return t.Extra[at+1 : at+1+t.Extra[at]] }

// Text returns the source text of node i.
func (t *Tree) Text(i Index) string {
	s := t.Nodes[i].Span
	return t.File.Text[s.Start:s.End]
}

// FunctionDecl is a function declaration's children; Parameters is a list's
// Extra offset.
type FunctionDecl struct{ Name, Parameters, Result, Body Index }

func (t *Tree) FunctionDecl(i Index) FunctionDecl {
	n := t.Nodes[i]
	x := t.Extra[n.B : n.B+3]
	return FunctionDecl{Name: n.A, Parameters: x[0], Result: x[1], Body: x[2]}
}

// LetStatement is a let statement's children.
type LetStatement struct{ Name, Type, Value Index }

func (t *Tree) LetStatement(i Index) LetStatement {
	n := t.Nodes[i]
	return LetStatement{Name: n.A, Type: t.Extra[n.B], Value: t.Extra[n.B+1]}
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
