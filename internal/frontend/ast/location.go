package ast

import (
	"compiler/internal/source"
	"reflect"
)

// IsNilNode handles the Go interface nil trap: a non-nil interface holding a
// nil pointer is not equal to nil, so a plain `n == nil` check is insufficient.
func IsNilNode(n Node) bool {
	if n == nil {
		return true
	}
	v := reflect.ValueOf(n)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// LocOf safely returns the location of a node, handling nil interfaces
// and nil pointer receivers without panicking.
func LocOf(n Node) *source.Location {
	if IsNilNode(n) {
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
