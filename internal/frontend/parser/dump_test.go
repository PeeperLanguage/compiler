package parser

import (
	"fmt"
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/literal"
)

// dump writes the tree one node per line, children indented under their parent.
func dump(t *ast.Tree) string {
	var b strings.Builder
	dumpNode(t, &b, t.Root, 0)
	return b.String()
}

func line(b *strings.Builder, depth int, parts ...string) {
	b.WriteString(strings.Repeat("  ", depth))
	for _, part := range parts {
		b.WriteString(part)
	}
	b.WriteString("\n")
}

func dumpNode(t *ast.Tree, b *strings.Builder, i ast.Index, depth int) {
	n := t.Nodes[i]
	switch n.Kind {
	case ast.KindModule:
		line(b, depth, "module")
		for _, child := range t.List(n.A) {
			dumpNode(t, b, child, depth+1)
		}
		for _, child := range t.List(n.B) {
			dumpNode(t, b, child, depth+1)
		}
	case ast.KindImportDecl:
		parts := []string{"import ", t.Text(n.A)}
		if n.B != ast.None {
			parts = append(parts, " as ", t.Text(n.B))
		}
		line(b, depth, parts...)
	case ast.KindFunctionDecl:
		f := t.FunctionDecl(i)
		line(b, depth, "function ", t.Text(f.Name))
		for _, param := range t.List(f.Parameters) {
			line(b, depth+1, "parameter ", t.Text(t.Nodes[param].A), ": ", t.Text(t.Nodes[param].B))
		}
		if f.Result != ast.None {
			line(b, depth+1, "result ", t.Text(f.Result))
		}
		dumpNode(t, b, f.Body, depth+1)
	case ast.KindBlock:
		line(b, depth, "block")
		for _, s := range t.List(n.A) {
			dumpNode(t, b, s, depth+1)
		}
	case ast.KindLetStatement:
		let := t.LetStatement(i)
		parts := []string{"let ", t.Text(let.Name)}
		if let.Type != ast.None {
			parts = append(parts, ": ", t.Text(let.Type))
		}
		line(b, depth, parts...)
		dumpNode(t, b, let.Value, depth+1)
	case ast.KindReturnStatement:
		line(b, depth, "return")
		if n.A != ast.None {
			dumpNode(t, b, n.A, depth+1)
		}
	case ast.KindExpressionStatement:
		dumpNode(t, b, n.A, depth)
	case ast.KindNameReference, ast.KindNumberLiteral:
		line(b, depth, t.Text(i))
	case ast.KindStringLiteral:
		line(b, depth, fmt.Sprintf("%q", literal.Value(t.Text(i))))
	case ast.KindBinaryExpression:
		line(b, depth, "binary ", n.Op.String())
		dumpNode(t, b, n.A, depth+1)
		dumpNode(t, b, n.B, depth+1)
	case ast.KindCallExpression:
		line(b, depth, "call")
		dumpNode(t, b, n.A, depth+1)
		for _, arg := range t.List(n.B) {
			dumpNode(t, b, arg, depth+1)
		}
	case ast.KindInvalidExpression:
		line(b, depth, "invalid")
	}
}
