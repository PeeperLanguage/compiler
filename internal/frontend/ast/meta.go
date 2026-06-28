package ast

import "compiler/internal/source"

type CommentGroup struct {
	Text     string
	Location *source.Location
}

type Documented struct {
	Doc         *CommentGroup
	DeclSurface string
}

func (d *Documented) SetDocComment(doc *CommentGroup) {
	if d == nil {
		return
	}
	d.Doc = doc
}

func (d *Documented) GetDocComment() *CommentGroup {
	if d == nil {
		return nil
	}
	return d.Doc
}

func (d *Documented) SetDeclSurface(surface string) {
	if d == nil {
		return
	}
	d.DeclSurface = surface
}

func (d *Documented) GetDeclSurface() string {
	if d == nil {
		return ""
	}
	return d.DeclSurface
}

type Attribute struct {
	Name     string
	Args     []string
	Location *source.Location
}

type Attributed struct {
	Attributes []Attribute
}

func (a *Attributed) SetAttributes(attrs []Attribute) {
	if a == nil {
		return
	}
	a.Attributes = attrs
}

func (a *Attributed) GetAttributes() []Attribute {
	if a == nil {
		return nil
	}
	return a.Attributes
}
