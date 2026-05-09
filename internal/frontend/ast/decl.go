package ast

import (
	"compiler/core/source"
)

type ImportDecl struct {
	Path     Expr
	Alias    *Ident
	Location source.Location
}

func (*ImportDecl) declNode()              {}
func (d *ImportDecl) Loc() source.Location { return d.Location }