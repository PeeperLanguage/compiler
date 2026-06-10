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

type Attribute struct {
	Name     string
	Args     []string
	Location *source.Location
}
