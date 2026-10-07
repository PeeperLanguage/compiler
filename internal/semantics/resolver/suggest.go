package resolver

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/pkg/colors"
)

func reportUnresolved(scope *symbols.Scope, node *ast.Ident, diag *diagnostics.DiagnosticBag) {
	if node == nil || diag == nil {
		return
	}
	msg := "unknown identifier `" + node.Name + "`"
	d := diagnostics.NewError(msg).
		WithCode(diagnostics.ErrUndefinedSymbol).
		WithPrimaryLabel(ast.LocOf(node), msg)
	if match, ok := nearestSymbolName(node.Name, scope); ok {
		d.WithText("help", "did you mean `"+match+"`?", colors.GREEN)
	}
	diag.Add(d)
}

func nearestSymbolName(name string, scope *symbols.Scope) (string, bool) {
	candidates := make([]diagnostics.NameCandidate, 0)
	seen := make(map[string]struct{})
	scopeDepth := 0
	for sc := scope; sc != nil; sc = sc.Parent() {
		for _, sym := range sc.Symbols() {
			if sym == nil || sym.Name == "" {
				continue
			}
			if _, ok := seen[sym.Name]; ok {
				continue
			}
			seen[sym.Name] = struct{}{}
			candidates = append(candidates, diagnostics.NameCandidate{
				Name:     sym.Name,
				Priority: scopeDepth,
			})
		}
		scopeDepth++
	}
	return diagnostics.NearestNameWithPriority(name, candidates)
}
