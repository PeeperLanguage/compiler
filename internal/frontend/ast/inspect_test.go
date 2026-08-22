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

func TestIndexIncludesNestedNodes(t *testing.T) {
	name := &Ident{NodeIDHolder: NodeIDHolder{NodeID: 2}, Name: "main"}
	result := &NumberLit{NodeIDHolder: NodeIDHolder{NodeID: 5}, Value: "0"}
	ret := &ReturnStmt{NodeIDHolder: NodeIDHolder{NodeID: 4}, Value: result}
	body := &BlockStmt{NodeIDHolder: NodeIDHolder{NodeID: 3}, Stmts: []Stmt{ret}}
	fn := &FnDecl{NodeIDHolder: NodeIDHolder{NodeID: 1}, Name: name, Body: body}

	nodes := Index(&Module{Stmts: []Stmt{fn}})
	for id, want := range map[NodeID]Node{1: fn, 2: name, 3: body, 4: ret, 5: result} {
		if nodes[id] != want {
			t.Fatalf("node %d = %#v, want %#v", id, nodes[id], want)
		}
	}
}
