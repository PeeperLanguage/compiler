// Package ast is the syntax tree of one file, stored flat: every node is a
// value in one array and refers to its children by index. A tree takes a few
// allocations, it holds no pointers for the garbage collector to scan, and
// later phases keep their results in slices indexed by node.
//
// What a node's A and B slots hold is declared once, by the struct of its kind
// in nodes.go; the code that reads and writes the slots is generated from those
// structs (see gen), so no index is written by hand.
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

// List is the Extra offset of a list: its length, then its items.
type List Index

// EmptyList is the offset every empty list shares: Extra[0] is 0, a list of
// no items.
const EmptyList List = 0

// Slots are the A and B slots of a node, as a packer returns them.
type Slots struct{ A, B Index }

// Kind is what a node is. The struct in nodes.go that names a kind in its
// ast:node directive says what the kind's children are and where they are kept.
type Kind uint8

//go:generate go run ./gen

const (
	KindNone                 Kind = iota // the placeholder at index 0
	KindModule                           // a file
	KindImportDecl                       // `import "pkg:path" as alias;`
	KindFunctionDecl                     // `fn`; also an interface method, without body and metadata
	KindConstDecl                        // `const`
	KindStructDecl                       // `struct Name { ... }`
	KindEnumDecl                         // `enum Name { ... }`
	KindInterfaceDecl                    // `iface Name { ... }`
	KindTypeAliasDecl                    // `type Name = T;`
	KindMetadata                         // the doc comment and attributes before a declaration
	KindDocComment                       // the `///` lines before a declaration; its text is the node's source text
	KindAttribute                        // `#[name(arguments)]`
	KindParameter                        // Op: MUT or 0
	KindDeclaredName                     // the name a declaration introduces; its text is the node's source text
	KindTypeName                         // a type written as a name; its text is the node's source text
	KindPath                             // `a::B<T>`
	KindGenericType                      // `Box<T>`
	KindReferenceType                    // `&T` or `&mut T`; Op: MUT or 0
	KindOptionalType                     // `?T`
	KindOwnedPointerType                 // `*T`
	KindRawPointerType                   // the type `rawptr`
	KindFixedArrayType                   // `[4]T`
	KindDynamicArrayType                 // `[]T`
	KindSliceType                        // `[..]T`
	KindFunctionType                     // `fn(T) -> R from a`
	KindStructType                       // `struct { ... }`
	KindField                            // `name: T` in a struct
	KindEnumType                         // `enum { ... }`
	KindVariant                          // `Name` or `Name: T` in an enum
	KindInterfaceType                    // `iface { ... }`
	KindInvalidType                      // a type that could not be parsed
	KindBlock                            // `{ ... }`
	KindLetStatement                     // `let`; Op: MUT or 0
	KindReturnStatement                  // `return`
	KindExpressionStatement              // an expression followed by `;`
	KindAssignStatement                  // `target = value;`
	KindDestructureStatement             // `let .{ a, b as c } = value;`
	KindIfStatement                      // `if condition { } else { }`
	KindLoopStatement                    // `for { }` or `for condition { }`
	KindForInStatement                   // `for value in items { }` or `for index, value in items { }`
	KindBreakStatement                   // `break;`
	KindContinueStatement                // `continue;`
	KindMatchStatement                   // `match subject { arms }`
	KindMatchArm                         // `pattern => { }`
	KindWildcardPattern                  // `_`
	KindNonePattern                      // `none`
	KindCasePattern                      // `Enum::Case` or `Enum::Case with pattern`
	KindStructPattern                    // `.{ a, b as c, _ }`
	KindFieldPattern                     // `a` or `a as b` in a struct pattern
	KindRestPattern                      // the `_` that ends a struct pattern
	KindNameReference                    // a use of a declared name; its text is the node's source text
	KindNumberLiteral                    // the number is the node's source text
	KindStringLiteral                    // Op: CSTRING (c"...") or 0; the literal is the node's source text, literal.Value decodes it
	KindCharacterLiteral                 // Op: BYTE_CHAR (b'x') or 0; the literal is the node's source text, literal.Value decodes it
	KindBoolLiteral                      // `true` or `false`, the node's source text
	KindNoneLiteral                      // `none`
	KindFieldName                        // a field or method name after `.`, or before `=` in a struct literal
	KindInferredLength                   // the `_` of `[_]T{...}`
	KindBinaryExpression                 // Op: the operator
	KindUnaryExpression                  // Op: PLUS, MINUS, BANG or TILDE
	KindReferenceExpression              // `&x` or `&mut x`; Op: MUT or 0
	KindRawAddressExpression             // `@x`
	KindFreeExpression                   // `free(x)`
	KindPrintExpression                  // `print(x)` or `println(x)`; Op: PRINT or PRINTLN
	KindCallExpression                   // `f(arguments)`
	KindSelectorExpression               // `x.name`
	KindIndexExpression                  // `x[index]`
	KindRange                            // `a..b` or `a..=b` inside `[ ]` or after `in`; Op: DOTDOT or DOTDOT_EQ
	KindStructLiteral                    // `Type.{ name = value }` or `.{ ... }`
	KindFieldValue                       // `name = value` in a struct literal
	KindVariantLiteral                   // `Enum::Variant with payload`
	KindArrayLiteral                     // `[N]T{ values }`
	KindCastExpression                   // `x as T`
	KindIsExpression                     // `x is Enum::Variant`
	KindOrElseExpression                 // `x orelse fallback`
	KindPipeExpression                   // `x |> f(arguments)`, kept as written
	KindParentheses                      // `( expression )`
	KindInvalidExpression                // an expression that could not be parsed
)

var kindNames = [...]string{
	KindNone:                 "none",
	KindModule:               "module",
	KindImportDecl:           "import declaration",
	KindFunctionDecl:         "function declaration",
	KindConstDecl:            "const declaration",
	KindStructDecl:           "struct declaration",
	KindEnumDecl:             "enum declaration",
	KindInterfaceDecl:        "interface declaration",
	KindTypeAliasDecl:        "type alias declaration",
	KindMetadata:             "metadata",
	KindDocComment:           "doc comment",
	KindAttribute:            "attribute",
	KindParameter:            "parameter",
	KindDeclaredName:         "declared name",
	KindTypeName:             "type name",
	KindPath:                 "path",
	KindGenericType:          "generic type",
	KindReferenceType:        "reference type",
	KindOptionalType:         "optional type",
	KindOwnedPointerType:     "owned pointer type",
	KindRawPointerType:       "raw pointer type",
	KindFixedArrayType:       "fixed array type",
	KindDynamicArrayType:     "dynamic array type",
	KindSliceType:            "slice type",
	KindFunctionType:         "function type",
	KindStructType:           "struct type",
	KindField:                "field",
	KindEnumType:             "enum type",
	KindVariant:              "variant",
	KindInterfaceType:        "interface type",
	KindInvalidType:          "invalid type",
	KindBlock:                "block",
	KindLetStatement:         "let statement",
	KindReturnStatement:      "return statement",
	KindExpressionStatement:  "expression statement",
	KindAssignStatement:      "assign statement",
	KindDestructureStatement: "destructure statement",
	KindIfStatement:          "if statement",
	KindLoopStatement:        "loop statement",
	KindForInStatement:       "for in statement",
	KindBreakStatement:       "break statement",
	KindContinueStatement:    "continue statement",
	KindMatchStatement:       "match statement",
	KindMatchArm:             "match arm",
	KindWildcardPattern:      "wildcard pattern",
	KindNonePattern:          "none pattern",
	KindCasePattern:          "case pattern",
	KindStructPattern:        "struct pattern",
	KindFieldPattern:         "field pattern",
	KindRestPattern:          "rest pattern",
	KindNameReference:        "name reference",
	KindNumberLiteral:        "number literal",
	KindStringLiteral:        "string literal",
	KindCharacterLiteral:     "character literal",
	KindBoolLiteral:          "bool literal",
	KindNoneLiteral:          "none literal",
	KindFieldName:            "field name",
	KindInferredLength:       "inferred length",
	KindBinaryExpression:     "binary expression",
	KindUnaryExpression:      "unary expression",
	KindReferenceExpression:  "reference expression",
	KindRawAddressExpression: "raw address expression",
	KindFreeExpression:       "free expression",
	KindPrintExpression:      "print expression",
	KindCallExpression:       "call expression",
	KindSelectorExpression:   "selector expression",
	KindIndexExpression:      "index expression",
	KindRange:                "range",
	KindStructLiteral:        "struct literal",
	KindFieldValue:           "field value",
	KindVariantLiteral:       "variant literal",
	KindArrayLiteral:         "array literal",
	KindCastExpression:       "cast expression",
	KindIsExpression:         "is expression",
	KindOrElseExpression:     "orelse expression",
	KindPipeExpression:       "pipe expression",
	KindParentheses:          "parentheses",
	KindInvalidExpression:    "invalid expression",
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

// List returns the items of list l.
func (t *Tree) List(l List) []Index { return t.Extra[l+1 : Index(l)+1+t.Extra[l]] }

// Text returns the source text of node i.
func (t *Tree) Text(i Index) string {
	s := t.Nodes[i].Span
	return t.File.Text[s.Start:s.End]
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
