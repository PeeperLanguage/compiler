package ast

import (
	"compiler/internal/source"
	"compiler/pkg/typednil"
)

// LocOf safely returns the location of a node, handling nil interfaces
// and nil pointer receivers without panicking.
func LocOf(n Node) *source.Location {
	if typednil.IsNil(n) {
		return nil
	}
	return n.loc()
}

// StartOf returns the start position of a node, or a zero position if
// the node or its location is nil.
func StartOf(n Node) source.Position {
	if loc := LocOf(n); loc != nil && loc.Start != nil {
		return *loc.Start
	}
	return source.NewPosition()
}

// EndOf returns the end position of a node, or a zero position if
// the node or its location is nil.
func EndOf(n Node) source.Position {
	if loc := LocOf(n); loc != nil && loc.End != nil {
		return *loc.End
	}
	return source.NewPosition()
}
