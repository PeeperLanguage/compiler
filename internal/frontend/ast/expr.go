package ast

import (
	"compiler/core/source"
)

type Ident struct {
	NodeIDHolder
	Name     string
	Location *source.Location
}

func (*Ident) exprNode()               {}
func (e *Ident) Loc() *source.Location { return e.Location }

type ScopeResolution struct {
	NodeIDHolder
	Module   *Ident
	Name     *Ident
	Location *source.Location
}

func (*ScopeResolution) exprNode()               {}
func (*ScopeResolution) typeNode()               {}
func (e *ScopeResolution) Loc() *source.Location { return e.Location }

type SelectorExpr struct {
	NodeIDHolder
	Expr     Expr
	Name     *Ident
	Location *source.Location
}

func (*SelectorExpr) exprNode()               {}
func (e *SelectorExpr) Loc() *source.Location { return e.Location }

type StructLitField struct {
	Name     *Ident
	Value    Expr
	Location *source.Location
}

type StructLit struct {
	NodeIDHolder
	Fields   []StructLitField
	Location *source.Location
}

func (*StructLit) exprNode()               {}
func (e *StructLit) Loc() *source.Location { return e.Location }

type BadExpr struct {
	NodeIDHolder
	Location *source.Location
}

func (*BadExpr) exprNode()               {}
func (e *BadExpr) Loc() *source.Location { return e.Location }

type NumberLit struct {
	NodeIDHolder
	Value    string
	Location *source.Location
}

func (*NumberLit) exprNode()               {}
func (e *NumberLit) Loc() *source.Location { return e.Location }

type StringLit struct {
	NodeIDHolder
	Value    string
	Location *source.Location
}

func (*StringLit) exprNode()               {}
func (e *StringLit) Loc() *source.Location { return e.Location }

type UnaryExpr struct {
	NodeIDHolder
	Op       string
	Expr     Expr
	Location *source.Location
}

func (*UnaryExpr) exprNode()               {}
func (e *UnaryExpr) Loc() *source.Location { return e.Location }

type BorrowExpr struct {
	NodeIDHolder
	Mutable  bool
	Expr     Expr
	Location *source.Location
}

func (*BorrowExpr) exprNode()               {}
func (e *BorrowExpr) Loc() *source.Location { return e.Location }

type BinaryExpr struct {
	NodeIDHolder
	Left     Expr
	Op       string
	Right    Expr
	Location *source.Location
}

func (*BinaryExpr) exprNode()               {}
func (e *BinaryExpr) Loc() *source.Location { return e.Location }

type CallExpr struct {
	NodeIDHolder
	Callee   Expr
	Args     []Expr
	Location *source.Location
}

func (*CallExpr) exprNode()               {}
func (e *CallExpr) Loc() *source.Location { return e.Location }

type AsExpr struct {
	NodeIDHolder
	Expr     Expr
	TypeExpr TypeExpr
	Location *source.Location
}

func (*AsExpr) exprNode()               {}
func (e *AsExpr) Loc() *source.Location { return e.Location }
