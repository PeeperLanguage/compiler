package ast

import (
	"compiler/internal/source"
)

type Ident struct {
	NodeIDHolder
	Name     string
	Location *source.Location
}

func (*Ident) exprNode()               {}
func (e *Ident) loc() *source.Location { return e.Location }

type ScopeResolution struct {
	NodeIDHolder
	Module   *Ident
	Name     *Ident
	Location *source.Location
}

func (*ScopeResolution) exprNode()               {}
func (*ScopeResolution) typeNode()               {}
func (e *ScopeResolution) loc() *source.Location { return e.Location }

type SelectorExpr struct {
	NodeIDHolder
	Expr     Expr
	Name     *Ident
	Location *source.Location
}

func (*SelectorExpr) exprNode()               {}
func (e *SelectorExpr) loc() *source.Location { return e.Location }

type StructLitField struct {
	Name     *Ident
	Value    Expr
	Location *source.Location
}

type StructLit struct {
	NodeIDHolder
	Type     TypeExpr
	Fields   []StructLitField
	Location *source.Location
}

func (*StructLit) exprNode()               {}
func (e *StructLit) loc() *source.Location { return e.Location }

type BadExpr struct {
	NodeIDHolder
	Location *source.Location
}

func (*BadExpr) exprNode()               {}
func (e *BadExpr) loc() *source.Location { return e.Location }

type NumberLit struct {
	NodeIDHolder
	Value    string
	Location *source.Location
}

func (*NumberLit) exprNode()               {}
func (e *NumberLit) loc() *source.Location { return e.Location }

type StringLit struct {
	NodeIDHolder
	Value    string
	Location *source.Location
}

func (*StringLit) exprNode()               {}
func (e *StringLit) loc() *source.Location { return e.Location }

type UnaryExpr struct {
	NodeIDHolder
	Op       string
	Expr     Expr
	Location *source.Location
}

func (*UnaryExpr) exprNode()               {}
func (e *UnaryExpr) loc() *source.Location { return e.Location }

type BinaryExpr struct {
	NodeIDHolder
	Left     Expr
	Op       string
	Right    Expr
	Location *source.Location
}

func (*BinaryExpr) exprNode()               {}
func (e *BinaryExpr) loc() *source.Location { return e.Location }

type CallExpr struct {
	NodeIDHolder
	Callee   Expr
	Args     []Expr
	Location *source.Location
}

func (*CallExpr) exprNode()               {}
func (e *CallExpr) loc() *source.Location { return e.Location }

type AsExpr struct {
	NodeIDHolder
	Expr     Expr
	TypeExpr TypeExpr
	Location *source.Location
}

func (*AsExpr) exprNode()               {}
func (e *AsExpr) loc() *source.Location { return e.Location }
