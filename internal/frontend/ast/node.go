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

type Decl interface {
	Node
	declNode()
	SetDocComment(*CommentGroup)
    GetDocComment() *CommentGroup
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
	FilePath string
	Doc      *CommentGroup
	Imports  []*ImportDecl
	Stmts    []Stmt
}
