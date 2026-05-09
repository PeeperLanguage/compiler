package ast

import "compiler/core/source"

type CommentGroup struct {
	Text     string
	Location source.Location
}

type Attribute struct {
	Name     string
	Args     []string
	Location source.Location
}
