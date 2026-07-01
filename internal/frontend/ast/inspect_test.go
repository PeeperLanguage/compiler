package ast

import (
	"strings"
	"testing"

	"compiler/internal/source"
)

type unknownNode struct {
	NodeIDHolder
}

func (n *unknownNode) loc() *source.Location { return nil }

func TestInspectPanicsOnUnhandledNodeType(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic for unhandled node type")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic = %T, want string", r)
		}
		if !strings.Contains(msg, "unhandled node type") {
			t.Fatalf("panic = %q, want unhandled node message", msg)
		}
	}()

	Inspect(&unknownNode{}, func(Node) bool { return true })
}

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
