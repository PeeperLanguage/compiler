package parser

import (
	"strings"

	"compiler/internal/frontend/ast"
)

// dump writes the tree one node per line, children indented under their
// parent: the kind, then its operator or `mut` if it has one, then the source
// text of a node without children.
func dump(t *ast.Tree) string {
	var b strings.Builder
	dumpNode(t, &b, t.Root, 0)
	return b.String()
}

func dumpNode(t *ast.Tree, b *strings.Builder, i ast.Index, depth int) {
	n := t.Nodes[i]
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString(n.Kind.String())
	if n.Op != 0 {
		b.WriteString(" ")
		b.WriteString(n.Op.String())
	}
	leaf := true
	for child := range t.Children(i) {
		if leaf {
			b.WriteString("\n")
			leaf = false
		}
		dumpNode(t, b, child, depth+1)
	}
	if leaf {
		b.WriteString(" ")
		b.WriteString(t.Text(i))
		b.WriteString("\n")
	}
}
