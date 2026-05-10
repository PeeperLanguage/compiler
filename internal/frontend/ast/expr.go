package ast

import (
	"compiler/core/source"
)

type Ident struct {
	Name     string
	Location source.Location
}

func (*Ident) exprNode()              {}
func (e *Ident) Loc() source.Location { return e.Location }

type BadExpr struct {
	Location source.Location
}

func (*BadExpr) exprNode()              {}
func (e *BadExpr) Loc() source.Location { return e.Location }

type NumberLit struct {
	Value    string
	Location source.Location
}

func (*NumberLit) exprNode()              {}
func (e *NumberLit) Loc() source.Location { return e.Location }

type StringLit struct {
	Value    string
	Location source.Location
}

func (*StringLit) exprNode()              {}
func (e *StringLit) Loc() source.Location { return e.Location }

type UnaryExpr struct {
	Op       string
	Expr     Expr
	Location source.Location
}

func (*UnaryExpr) exprNode()              {}
func (e *UnaryExpr) Loc() source.Location { return e.Location }

type BinaryExpr struct {
	Left     Expr
	Op       string
	Right    Expr
	Location source.Location
}

func (*BinaryExpr) exprNode()              {}
func (e *BinaryExpr) Loc() source.Location { return e.Location }

type CallExpr struct {
	Callee   Expr
	Args     []Expr
	Location source.Location
}

func (*CallExpr) exprNode()              {}
func (e *CallExpr) Loc() source.Location { return e.Location }
