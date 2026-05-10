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

type NamedType struct {
	Name     string
	Location source.Location
}

func (*NamedType) typeNode()              {}
func (t *NamedType) Loc() source.Location { return t.Location }

type Param struct {
	Name     *Ident
	Type     TypeExpr
	Location source.Location
}

type TypeParam struct {
	Name     *Ident
	Location source.Location
}

type LetDecl struct {
	Name        *Ident
	Type        TypeExpr
	Value       Expr
	IsMutable   bool
	IsModuleVar bool
	Location    source.Location
}

func (*LetDecl) declNode()              {}
func (d *LetDecl) Loc() source.Location { return d.Location }
func (*LetDecl) stmtNode()              {}

type ConstDecl struct {
	Name        *Ident
	Type        TypeExpr
	Value       Expr
	IsModuleVar bool
	Location    source.Location
}

func (*ConstDecl) declNode()              {}
func (d *ConstDecl) Loc() source.Location { return d.Location }
func (*ConstDecl) stmtNode()              {}

type FnDecl struct {
	Receiver   *Param
	Name       *Ident
	TypeParams []TypeParam
	Params     []Param
	ReturnType TypeExpr
	Body       *BlockStmt
	Location   source.Location
}

func (*FnDecl) declNode()              {}
func (d *FnDecl) Loc() source.Location { return d.Location }
