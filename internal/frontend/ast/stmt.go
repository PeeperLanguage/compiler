package ast

import "compiler/core/source"

type BlockStmt struct {
	Stmts    []Stmt
	Location *source.Location
}

func (*BlockStmt) stmtNode()              {}
func (s *BlockStmt) Loc() *source.Location { return s.Location }

type ExprStmt struct {
	Expr     Expr
	Location *source.Location
}

func (*ExprStmt) stmtNode()              {}
func (s *ExprStmt) Loc() *source.Location { return s.Location }

type ReturnStmt struct {
	Value    Expr
	Location *source.Location
}

func (*ReturnStmt) stmtNode()              {}
func (s *ReturnStmt) Loc() *source.Location { return s.Location }

type IfStmt struct {
	Cond     Expr
	Then     *BlockStmt
	Else     Stmt
	Location *source.Location
}

func (*IfStmt) stmtNode()              {}
func (s *IfStmt) Loc() *source.Location { return s.Location }
