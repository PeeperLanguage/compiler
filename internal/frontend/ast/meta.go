package ast

import "compiler/pkg/source"

type CommentGroup struct {
	Text     string
	Location *source.Location
}

type Attribute struct {
	Name     string
	Args     []string
	Location *source.Location
}
