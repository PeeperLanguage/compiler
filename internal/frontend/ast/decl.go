package ast

import (
	"compiler/internal/source"
)

type ImportDecl struct {
	NodeIDHolder
	Path     Expr
	Alias    *Ident
	Location *source.Location
}

func (*ImportDecl) declNode()               {}
func (d *ImportDecl) loc() *source.Location { return d.Location }

type NamedType struct {
	NodeIDHolder
	Name     string
	Location *source.Location
}

func (*NamedType) typeNode()               {}
func (t *NamedType) loc() *source.Location { return t.Location }

type RawPtrType struct {
	NodeIDHolder
	Mutable  bool
	Target   TypeExpr
	Location *source.Location
}

func (*RawPtrType) typeNode()               {}
func (t *RawPtrType) loc() *source.Location { return t.Location }

type FuncType struct {
	NodeIDHolder
	Params   []TypeExpr
	Return   TypeExpr
	Location *source.Location
}

func (*FuncType) typeNode()               {}
func (t *FuncType) loc() *source.Location { return t.Location }

type StructType struct {
	NodeIDHolder
	Fields   []TypeField
	Location *source.Location
}

func (*StructType) typeNode()               {}
func (t *StructType) loc() *source.Location { return t.Location }

type InterfaceType struct {
	NodeIDHolder
	Methods  []TypeMethod
	Location *source.Location
}

func (*InterfaceType) typeNode()               {}
func (t *InterfaceType) loc() *source.Location { return t.Location }

type EnumType struct {
	NodeIDHolder
	Variants []EnumVariant
	Location *source.Location
}

func (*EnumType) typeNode()               {}
func (t *EnumType) loc() *source.Location { return t.Location }

type TypeField struct {
	Name     *Ident
	Type     TypeExpr
	Location *source.Location
}

type TypeMethod struct {
	Name       *Ident
	TypeParams []TypeParam
	Params     []Param
	ReturnType TypeExpr
	Location   *source.Location
}

type EnumVariant struct {
	Name     *Ident
	Location *source.Location
}

type Param struct {
	Name     *Ident
	Type     TypeExpr
	Location *source.Location
}

type TypeParam struct {
	Name     *Ident
	Location *source.Location
}

type LetDecl struct {
	NodeIDHolder
	Name        *Ident
	Type        TypeExpr
	Value       Expr
	IsMutable   bool
	IsModuleVar bool
	Location    *source.Location
}

func (*LetDecl) declNode()               {}
func (d *LetDecl) loc() *source.Location { return d.Location }
func (*LetDecl) stmtNode()               {}

type ConstDecl struct {
	NodeIDHolder
	Name        *Ident
	Type        TypeExpr
	Value       Expr
	IsModuleVar bool
	Location    *source.Location
}

func (*ConstDecl) declNode()               {}
func (d *ConstDecl) loc() *source.Location { return d.Location }
func (*ConstDecl) stmtNode()               {}

type FnDecl struct {
	NodeIDHolder
	Name       *Ident
	TypeParams []TypeParam
	Params     []Param
	ReturnType TypeExpr
	Body       *BlockStmt
	Location   *source.Location
}

func (*FnDecl) declNode()               {}
func (d *FnDecl) loc() *source.Location { return d.Location }

type TypeAliasDecl struct {
	NodeIDHolder
	Name       *Ident
	TypeParams []TypeParam
	Type       TypeExpr
	Location   *source.Location
}

func (*TypeAliasDecl) declNode()               {}
func (d *TypeAliasDecl) loc() *source.Location { return d.Location }

type StructDecl struct {
	NodeIDHolder
	Name       *Ident
	TypeParams []TypeParam
	Fields     []TypeField
	Location   *source.Location
}

func (*StructDecl) declNode()               {}
func (d *StructDecl) loc() *source.Location { return d.Location }

type InterfaceDecl struct {
	NodeIDHolder
	Name       *Ident
	TypeParams []TypeParam
	Methods    []TypeMethod
	Location   *source.Location
}

func (*InterfaceDecl) declNode()               {}
func (d *InterfaceDecl) loc() *source.Location { return d.Location }

type EnumDecl struct {
	NodeIDHolder
	Name       *Ident
	TypeParams []TypeParam
	Variants   []EnumVariant
	Location   *source.Location
}

func (*EnumDecl) declNode()               {}
func (d *EnumDecl) loc() *source.Location { return d.Location }

type ImplDecl struct {
	NodeIDHolder
	Target   TypeExpr
	Methods  []*FnDecl
	Location *source.Location
}

func (*ImplDecl) declNode()               {}
func (d *ImplDecl) loc() *source.Location { return d.Location }
