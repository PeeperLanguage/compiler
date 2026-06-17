package ast

import "compiler/internal/source"

type BlockStmt struct {
	NodeIDHolder
	Documented
	Stmts    []Stmt
	Location *source.Location
}

func (*BlockStmt) stmtNode()               {}
func (s *BlockStmt) loc() *source.Location { return s.Location }

type ExprStmt struct {
	NodeIDHolder
	Documented
	Expr     Expr
	Location *source.Location
}

func (*ExprStmt) stmtNode()               {}
func (s *ExprStmt) loc() *source.Location { return s.Location }

type AssignStmt struct {
	NodeIDHolder
	Documented
	Target   Expr
	Value    Expr
	Location *source.Location
}

func (*AssignStmt) stmtNode()               {}
func (s *AssignStmt) loc() *source.Location { return s.Location }

type ReturnStmt struct {
	NodeIDHolder
	Documented
	Value    Expr
	Location *source.Location
}

func (*ReturnStmt) stmtNode()               {}
func (s *ReturnStmt) loc() *source.Location { return s.Location }

type BadStmt struct {
	NodeIDHolder
	Location *source.Location
}

func (*BadStmt) stmtNode()               {}
func (s *BadStmt) loc() *source.Location { return s.Location }

type IfStmt struct {
	NodeIDHolder
	Documented
	Cond     Expr
	Then     *BlockStmt
	Else     Stmt
	Location *source.Location
}

func (*IfStmt) stmtNode()               {}
func (s *IfStmt) loc() *source.Location { return s.Location }
