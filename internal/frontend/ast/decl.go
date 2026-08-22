package ast

import (
	"strings"

	"compiler/internal/source"
)

type ImportDecl struct {
	NodeIDHolder
	Documented
	Path     Expr
	Alias    *Ident
	Location *source.Location
}

func (*ImportDecl) declNode() {}
func (*ImportDecl) stmtNode() {}
func (d *ImportDecl) inspectChildren(visit func(Node)) {
	visit(d.Path)
	visit(d.Alias)
}
func (d *ImportDecl) loc() *source.Location { return d.Location }

type NamedType struct {
	NodeIDHolder
	Name     string
	Location *source.Location
}

func (*NamedType) typeNode()                  {}
func (*NamedType) inspectChildren(func(Node)) {}
func (t *NamedType) loc() *source.Location    { return t.Location }
func (t *NamedType) TypeText() string {
	if t == nil {
		return ""
	}
	return t.Name
}

type OwnedPtrType struct {
	NodeIDHolder
	Target   TypeExpr
	Location *source.Location
}

func (*OwnedPtrType) typeNode()                          {}
func (t *OwnedPtrType) inspectChildren(visit func(Node)) { visit(t.Target) }
func (t *OwnedPtrType) loc() *source.Location            { return t.Location }
func (t *OwnedPtrType) TypeText() string {
	if t == nil {
		return ""
	}
	return "*" + TypeText(t.Target)
}

type RawPtrType struct {
	NodeIDHolder
	Location *source.Location
}

func (*RawPtrType) typeNode()                  {}
func (*RawPtrType) inspectChildren(func(Node)) {}
func (t *RawPtrType) loc() *source.Location    { return t.Location }
func (t *RawPtrType) TypeText() string {
	if t == nil {
		return ""
	}
	return "rawptr"
}

type RefType struct {
	NodeIDHolder
	Mutable  bool
	Target   TypeExpr
	Location *source.Location
}

func (*RefType) typeNode()                          {}
func (t *RefType) inspectChildren(visit func(Node)) { visit(t.Target) }
func (t *RefType) loc() *source.Location            { return t.Location }
func (t *RefType) TypeText() string {
	if t == nil {
		return ""
	}
	prefix := "&"
	if t.Mutable {
		prefix = "&mut "
	}
	return prefix + TypeText(t.Target)
}

type OptionalType struct {
	NodeIDHolder
	Inner    TypeExpr
	Location *source.Location
}

func (*OptionalType) typeNode()                          {}
func (t *OptionalType) inspectChildren(visit func(Node)) { visit(t.Inner) }
func (t *OptionalType) loc() *source.Location            { return t.Location }
func (t *OptionalType) TypeText() string {
	if t == nil {
		return ""
	}
	return "?" + TypeText(t.Inner)
}

type ArrayShape uint8

const (
	ArrayFixed ArrayShape = iota
	ArrayOwner
	ArraySlice
)

type ArrayType struct {
	NodeIDHolder
	Len      *NumberLit
	Shape    ArrayShape
	Elem     TypeExpr
	Location *source.Location
}

func (*ArrayType) typeNode() {}
func (t *ArrayType) inspectChildren(visit func(Node)) {
	visit(t.Len)
	visit(t.Elem)
}
func (t *ArrayType) loc() *source.Location { return t.Location }
func (t *ArrayType) TypeText() string {
	if t == nil {
		return ""
	}
	switch t.Shape {
	case ArrayOwner:
		return "[]" + TypeText(t.Elem)
	case ArraySlice:
		return "[..]" + TypeText(t.Elem)
	}
	if t.Len == nil {
		return "[" + TypeText(t.Elem) + "]"
	}
	return "[" + t.Len.Value + "]" + TypeText(t.Elem)
}

type FuncType struct {
	NodeIDHolder
	Params        []Param
	Return        TypeExpr
	ReturnOrigins *ReturnOriginClause
	Location      *source.Location
}

func (*FuncType) typeNode() {}
func (t *FuncType) inspectChildren(visit func(Node)) {
	inspectParams(t.Params, visit)
	visit(t.Return)
	inspectReturnOrigins(t.ReturnOrigins, visit)
}
func (t *FuncType) loc() *source.Location { return t.Location }
func (t *FuncType) TypeText() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("fn(")
	for i, param := range t.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		if param.Name != nil {
			b.WriteString(param.Name.Name)
			b.WriteString(": ")
		}
		b.WriteString(TypeText(param.Type))
	}
	b.WriteString(")")
	if ret := TypeText(t.Return); ret != "" {
		b.WriteString(" -> ")
		b.WriteString(ret)
	}
	b.WriteString(t.ReturnOrigins.Text())
	return b.String()
}

type ReturnOriginClause struct {
	Sources  []*Ident
	Location *source.Location
}

func (c *ReturnOriginClause) Text() string {
	if c == nil || len(c.Sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" from ")
	if len(c.Sources) == 1 {
		b.WriteString(c.Sources[0].Name)
		return b.String()
	}
	b.WriteString("(")
	for i, source := range c.Sources {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(source.Name)
	}
	b.WriteString(")")
	return b.String()
}

type StructType struct {
	NodeIDHolder
	Fields   []TypeField
	Location *source.Location
}

func (*StructType) typeNode() {}
func (t *StructType) inspectChildren(visit func(Node)) {
	for _, field := range t.Fields {
		visit(field.Name)
		visit(field.Type)
	}
}
func (t *StructType) loc() *source.Location { return t.Location }
func (t *StructType) TypeText() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("struct {")
	for i, field := range t.Fields {
		if i > 0 {
			b.WriteString(", ")
		}
		if field.Name != nil {
			b.WriteString(field.Name.Name)
			b.WriteString(": ")
		}
		b.WriteString(TypeText(field.Type))
	}
	b.WriteString("}")
	return b.String()
}

type InterfaceType struct {
	NodeIDHolder
	Methods  []TypeMethod
	Location *source.Location
}

func (*InterfaceType) typeNode() {}
func (t *InterfaceType) inspectChildren(visit func(Node)) {
	for _, method := range t.Methods {
		visit(method.Name)
		if method.Receiver != nil {
			inspectParam(*method.Receiver, visit)
		}
		inspectTypeParams(method.TypeParams, visit)
		inspectParams(method.Params, visit)
		visit(method.ReturnType)
		inspectReturnOrigins(method.ReturnOrigins, visit)
	}
}
func (t *InterfaceType) loc() *source.Location { return t.Location }
func (t *InterfaceType) TypeText() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("iface {")
	for i, method := range t.Methods {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("fn (")
		if method.Receiver != nil {
			b.WriteString(TypeText(method.Receiver.Type))
		}
		b.WriteString(") ")
		if method.Name != nil {
			b.WriteString(method.Name.Name)
		}
		b.WriteString("(")
		for j, param := range method.Params {
			if j > 0 {
				b.WriteString(", ")
			}
			if param.Name != nil {
				b.WriteString(param.Name.Name)
				b.WriteString(": ")
			}
			b.WriteString(TypeText(param.Type))
		}
		b.WriteString(")")
		if ret := TypeText(method.ReturnType); ret != "" {
			b.WriteString(" -> ")
			b.WriteString(ret)
		}
		b.WriteString(method.ReturnOrigins.Text())
	}
	b.WriteString("}")
	return b.String()
}

type EnumType struct {
	NodeIDHolder
	Variants []EnumVariant
	Location *source.Location
}

func (*EnumType) typeNode() {}
func (t *EnumType) inspectChildren(visit func(Node)) {
	for _, variant := range t.Variants {
		visit(variant.Name)
	}
}
func (t *EnumType) loc() *source.Location { return t.Location }
func (t *EnumType) TypeText() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("enum {")
	for i, variant := range t.Variants {
		if i > 0 {
			b.WriteString(", ")
		}
		if variant.Name != nil {
			b.WriteString(variant.Name.Name)
		}
	}
	b.WriteString("}")
	return b.String()
}

type TypeField struct {
	Name     *Ident
	Type     TypeExpr
	Location *source.Location
}

type TypeMethod struct {
	Name          *Ident
	Receiver      *Param
	TypeParams    []TypeParam
	Params        []Param
	ReturnType    TypeExpr
	ReturnOrigins *ReturnOriginClause
	Location      *source.Location
}

type EnumVariant struct {
	Name     *Ident
	Location *source.Location
}

type Param struct {
	IsMutable bool
	Name      *Ident
	Type      TypeExpr
	Default   Expr
	Location  *source.Location
}

type TypeParam struct {
	Name     *Ident
	Location *source.Location
}

type LetDecl struct {
	NodeIDHolder
	Documented
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
func (d *LetDecl) inspectChildren(visit func(Node)) {
	visit(d.Name)
	visit(d.Type)
	visit(d.Value)
}

type ConstDecl struct {
	NodeIDHolder
	Documented
	Name        *Ident
	Type        TypeExpr
	Value       Expr
	IsModuleVar bool
	Location    *source.Location
}

func (*ConstDecl) declNode()               {}
func (d *ConstDecl) loc() *source.Location { return d.Location }
func (*ConstDecl) stmtNode()               {}
func (d *ConstDecl) inspectChildren(visit func(Node)) {
	visit(d.Name)
	visit(d.Type)
	visit(d.Value)
}

type FnDecl struct {
	NodeIDHolder
	Documented
	Attributed
	Name          *Ident
	Receiver      *Param
	TypeParams    []TypeParam
	Params        []Param
	ReturnType    TypeExpr
	ReturnOrigins *ReturnOriginClause
	Body          *BlockStmt
	Location      *source.Location
}

func (*FnDecl) declNode() {}
func (*FnDecl) stmtNode() {}
func (d *FnDecl) inspectChildren(visit func(Node)) {
	visit(d.Name)
	if d.Receiver != nil {
		inspectParam(*d.Receiver, visit)
	}
	inspectTypeParams(d.TypeParams, visit)
	inspectParams(d.Params, visit)
	visit(d.ReturnType)
	inspectReturnOrigins(d.ReturnOrigins, visit)
	visit(d.Body)
}
func (d *FnDecl) loc() *source.Location { return d.Location }
func (d *FnDecl) ParamsWithReceiver() []Param {
	if d == nil {
		return nil
	}
	if d.Receiver == nil {
		return d.Params
	}
	params := make([]Param, 0, len(d.Params)+1)
	params = append(params, *d.Receiver)
	return append(params, d.Params...)
}

type TypeAliasDecl struct {
	NodeIDHolder
	Documented
	Attributed
	Name       *Ident
	TypeParams []TypeParam
	Type       TypeExpr
	Location   *source.Location
}

func (*TypeAliasDecl) declNode() {}
func (*TypeAliasDecl) stmtNode() {}
func (d *TypeAliasDecl) inspectChildren(visit func(Node)) {
	visit(d.Name)
	inspectTypeParams(d.TypeParams, visit)
	visit(d.Type)
}
func (d *TypeAliasDecl) loc() *source.Location { return d.Location }
func (d *TypeAliasDecl) DeclName() *Ident      { return d.Name }
func (d *TypeAliasDecl) UnderlyingType() TypeExpr {
	return d.Type
}

type StructDecl struct {
	NodeIDHolder
	Documented
	Attributed
	Name       *Ident
	TypeParams []TypeParam
	// Type holds the canonical payload for the declaration.
	// Parser must always populate this with *StructType so later phases can
	// treat declaration syntax and anonymous struct syntax uniformly.
	Type     TypeExpr
	Location *source.Location
}

func (*StructDecl) declNode() {}
func (*StructDecl) stmtNode() {}
func (d *StructDecl) inspectChildren(visit func(Node)) {
	visit(d.Name)
	inspectTypeParams(d.TypeParams, visit)
	visit(d.Type)
}
func (d *StructDecl) loc() *source.Location { return d.Location }
func (d *StructDecl) DeclName() *Ident      { return d.Name }
func (d *StructDecl) UnderlyingType() TypeExpr {
	return d.Type
}

type InterfaceDecl struct {
	NodeIDHolder
	Documented
	Attributed
	Name       *Ident
	TypeParams []TypeParam
	// Type holds the canonical payload for the declaration.
	// Parser must always populate this with *InterfaceType.
	Type     TypeExpr
	Location *source.Location
}

func (*InterfaceDecl) declNode() {}
func (*InterfaceDecl) stmtNode() {}
func (d *InterfaceDecl) inspectChildren(visit func(Node)) {
	visit(d.Name)
	inspectTypeParams(d.TypeParams, visit)
	visit(d.Type)
}
func (d *InterfaceDecl) loc() *source.Location { return d.Location }
func (d *InterfaceDecl) DeclName() *Ident      { return d.Name }
func (d *InterfaceDecl) UnderlyingType() TypeExpr {
	return d.Type
}

type EnumDecl struct {
	NodeIDHolder
	Documented
	Attributed
	Name       *Ident
	TypeParams []TypeParam
	// Type holds the canonical payload for the declaration.
	// Parser must always populate this with *EnumType.
	Type     TypeExpr
	Location *source.Location
}

func (*EnumDecl) declNode() {}
func (*EnumDecl) stmtNode() {}
func (d *EnumDecl) inspectChildren(visit func(Node)) {
	visit(d.Name)
	inspectTypeParams(d.TypeParams, visit)
	visit(d.Type)
}
func (d *EnumDecl) loc() *source.Location { return d.Location }
func (d *EnumDecl) DeclName() *Ident      { return d.Name }
func (d *EnumDecl) UnderlyingType() TypeExpr {
	return d.Type
}

type BadDecl struct {
	NodeIDHolder
	Documented
	Location *source.Location
}

func (*BadDecl) declNode()                  {}
func (*BadDecl) stmtNode()                  {}
func (*BadDecl) inspectChildren(func(Node)) {}
func (d *BadDecl) loc() *source.Location    { return d.Location }

func inspectParam(param Param, visit func(Node)) {
	visit(param.Name)
	visit(param.Type)
	visit(param.Default)
}

func inspectParams(params []Param, visit func(Node)) {
	for _, param := range params {
		inspectParam(param, visit)
	}
}

func inspectTypeParams(params []TypeParam, visit func(Node)) {
	for _, param := range params {
		visit(param.Name)
	}
}

func inspectReturnOrigins(origins *ReturnOriginClause, visit func(Node)) {
	if origins == nil {
		return
	}
	for _, origin := range origins.Sources {
		visit(origin)
	}
}
