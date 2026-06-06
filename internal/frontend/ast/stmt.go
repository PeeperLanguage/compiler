package ast

import "compiler/core/source"

type BlockStmt struct {
	NodeIDHolder
	Stmts    []Stmt
	Location *source.Location
}

func (*BlockStmt) stmtNode()               {}
func (s *BlockStmt) Loc() *source.Location { return s.Location }

type ExprStmt struct {
	NodeIDHolder
	Expr     Expr
	Location *source.Location
}

func (*ExprStmt) stmtNode()               {}
func (s *ExprStmt) Loc() *source.Location { return s.Location }

type AssignStmt struct {
	NodeIDHolder
	Target   Expr
	Value    Expr
	Location *source.Location
}

func (*AssignStmt) stmtNode()               {}
func (s *AssignStmt) Loc() *source.Location { return s.Location }

type ReturnStmt struct {
	NodeIDHolder
	Value    Expr
	Location *source.Location
}

func (*ReturnStmt) stmtNode()               {}
func (s *ReturnStmt) Loc() *source.Location { return s.Location }

type IfStmt struct {
	NodeIDHolder
	Cond     Expr
	Then     *BlockStmt
	Else     Stmt
	Location *source.Location
}

func (*IfStmt) stmtNode()               {}
func (s *IfStmt) Loc() *source.Location { return s.Location }
