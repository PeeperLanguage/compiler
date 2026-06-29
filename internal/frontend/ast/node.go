package ast

import "compiler/internal/source"

type NodeID uint32

type NodeIDHolder struct {
	NodeID NodeID
}

func (h *NodeIDHolder) ID() NodeID      { return h.NodeID }
func (h *NodeIDHolder) SetID(id NodeID) { h.NodeID = id }

type Node interface {
	loc() *source.Location
	ID() NodeID
	SetID(NodeID)
}

type DocumentedNode interface {
	SetDocComment(*CommentGroup)
	GetDocComment() *CommentGroup
}

type AttributedNode interface {
	SetAttributes([]Attribute)
	GetAttributes() map[string]Attribute
	GetAttribute(string) (Attribute, bool)
}

// Decl is the common surface for module- or block-level declarations.
// It intentionally stays minimal: most declarations do not describe a named
// type, so type-specific behavior lives on the narrower TypeDecl interface.
type Decl interface {
	Node
	declNode()
	SetDocComment(*CommentGroup)
	GetDocComment() *CommentGroup
	SetDeclSurface(string)
	GetDeclSurface() string
}

// TypeDecl marks declarations that introduce a named type into module scope.
// The declaration wrapper carries naming/doc/scope concerns, while the
// canonical structure of the type lives in the returned TypeExpr payload.
// Keeping these roles separate lets parser syntax change later without
// rewriting the semantic phases that only care about "name + underlying type".
type TypeDecl interface {
	Decl
	AttributedNode
	DeclName() *Ident
	UnderlyingType() TypeExpr
}

type Stmt interface {
	Node
	stmtNode()
}

type Expr interface {
	Node
	exprNode()
}

type TypeExpr interface {
	Node
	typeNode()
}

type Module struct {
	FilePath          string
	Doc               *CommentGroup
	Imports           []*ImportDecl
	Stmts             []Stmt
	ImportFingerprint string
	ExportFingerprint string
}
