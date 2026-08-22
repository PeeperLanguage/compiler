package ast

import (
	"strings"
	"testing"
)

func TestInspectIndexExprVisitsBaseBeforeIndex(t *testing.T) {
	index := &IndexExpr{
		Expr:  &Ident{Name: "xs"},
		Index: &Ident{Name: "i"},
	}
	var names []string
	Inspect(index, func(n Node) bool {
		if ident, ok := n.(*Ident); ok {
			names = append(names, ident.Name)
		}
		return true
	})
	if got, want := strings.Join(names, ","), "xs,i"; got != want {
		t.Fatalf("inspect order = %q, want %q", got, want)
	}
}

func TestInspectPreservesExitAndPruningSemantics(t *testing.T) {
	tree := &BinaryExpr{
		Left:  &Ident{Name: "left"},
		Right: &UnaryExpr{Expr: &Ident{Name: "hidden"}},
	}
	var events []string
	Inspect(tree, func(node Node) bool {
		if node == nil {
			events = append(events, "exit")
			return true
		}
		switch node := node.(type) {
		case *Ident:
			events = append(events, node.Name)
		case *UnaryExpr:
			events = append(events, "unary")
			return false
		default:
			events = append(events, "binary")
		}
		return true
	})
	if got, want := strings.Join(events, ","), "binary,left,exit,unary,exit"; got != want {
		t.Fatalf("inspect events = %q, want %q", got, want)
	}
}
