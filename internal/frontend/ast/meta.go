package ast

import "compiler/internal/source"

type CommentGroup struct {
	Text     string
	Location *source.Location
}

type DocumentedNode interface {
	Node
	SetDocComment(*CommentGroup)
}

type Documented struct {
	Doc *CommentGroup
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

type Attribute struct {
	Name     string
	Args     []string
	Location *source.Location
}
