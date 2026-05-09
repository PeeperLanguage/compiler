package ast

import (
	"compiler/core/source"
)

type Ident struct {
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
