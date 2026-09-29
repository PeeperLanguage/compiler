package ast

import (
	"slices"
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

func TestInspectIncludesNestedNodes(t *testing.T) {
	name := &Ident{Name: "main"}
	result := &NumberLit{Value: "0"}
	ret := &ReturnStmt{Value: result}
	body := &BlockStmt{Stmts: []Stmt{ret}}
	fn := &FnDecl{Name: name, Body: body}

	var nodes []Node
	Inspect(fn, func(node Node) bool {
		if node != nil {
			nodes = append(nodes, node)
		}
		return true
	})
	want := []Node{fn, name, body, ret, result}
	if !slices.Equal(nodes, want) {
		t.Fatalf("inspect nodes = %v, want %v", nodes, want)
	}
}
