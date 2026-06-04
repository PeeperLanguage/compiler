package ast

import (
	"compiler/core/source"
)

type ImportDecl struct {
	Path     Expr
	Alias    *Ident
	Location *source.Location
}

func (*ImportDecl) declNode()               {}
func (d *ImportDecl) Loc() *source.Location { return d.Location }

type NamedType struct {
	Name     string
	Location *source.Location
}

func (*NamedType) typeNode()               {}
func (t *NamedType) Loc() *source.Location { return t.Location }

type RefType struct {
	Mutable  bool
	Target   TypeExpr
	Location *source.Location
}

func (*RefType) typeNode()               {}
func (t *RefType) Loc() *source.Location { return t.Location }

type RawPtrType struct {
	Mutable  bool
	Target   TypeExpr
	Location *source.Location
}

func (*RawPtrType) typeNode()               {}
func (t *RawPtrType) Loc() *source.Location { return t.Location }

type FuncType struct {
	Params   []TypeExpr
	Return   TypeExpr
	Location *source.Location
}

func (*FuncType) typeNode()               {}
func (t *FuncType) Loc() *source.Location { return t.Location }

type StructType struct {
	Fields   []TypeField
	Location *source.Location
}

func (*StructType) typeNode()               {}
func (t *StructType) Loc() *source.Location { return t.Location }

type InterfaceType struct {
	Methods  []TypeMethod
	Location *source.Location
}

func (*InterfaceType) typeNode()               {}
func (t *InterfaceType) Loc() *source.Location { return t.Location }

type EnumType struct {
	Variants []EnumVariant
	Location *source.Location
}

func (*EnumType) typeNode()               {}
func (t *EnumType) Loc() *source.Location { return t.Location }

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
	Name        *Ident
	Type        TypeExpr
	Value       Expr
	IsMutable   bool
	IsModuleVar bool
	Location    *source.Location
}

func (*LetDecl) declNode()               {}
func (d *LetDecl) Loc() *source.Location { return d.Location }
func (*LetDecl) stmtNode()               {}

type ConstDecl struct {
	Name        *Ident
	Type        TypeExpr
	Value       Expr
	IsModuleVar bool
	Location    *source.Location
}

func (*ConstDecl) declNode()               {}
func (d *ConstDecl) Loc() *source.Location { return d.Location }
func (*ConstDecl) stmtNode()               {}

type FnDecl struct {
	Receiver   *Param
	Name       *Ident
	TypeParams []TypeParam
	Params     []Param
	ReturnType TypeExpr
	Body       *BlockStmt
	Location   *source.Location
}

func (*FnDecl) declNode()               {}
func (d *FnDecl) Loc() *source.Location { return d.Location }

type TypeAliasDecl struct {
	Name       *Ident
	TypeParams []TypeParam
	Type       TypeExpr
	Location   *source.Location
}

func (*TypeAliasDecl) declNode()               {}
func (d *TypeAliasDecl) Loc() *source.Location { return d.Location }

type StructDecl struct {
	Name       *Ident
	TypeParams []TypeParam
	Fields     []TypeField
	Location   *source.Location
}

func (*StructDecl) declNode()               {}
func (d *StructDecl) Loc() *source.Location { return d.Location }

type InterfaceDecl struct {
	Name       *Ident
	TypeParams []TypeParam
	Methods    []TypeMethod
	Location   *source.Location
}

func (*InterfaceDecl) declNode()               {}
func (d *InterfaceDecl) Loc() *source.Location { return d.Location }

type EnumDecl struct {
	Name       *Ident
	TypeParams []TypeParam
	Variants   []EnumVariant
	Location   *source.Location
}

func (*EnumDecl) declNode()               {}
func (d *EnumDecl) Loc() *source.Location { return d.Location }
