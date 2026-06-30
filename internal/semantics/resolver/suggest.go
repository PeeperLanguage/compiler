package resolver

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/table"
	"compiler/pkg/colors"
)

func reportUnresolved(module *project.Module, scope *table.Scope, node *ast.Ident, diag *diagnostics.DiagnosticBag) bool {
	if module == nil || node == nil || diag == nil {
		return false
	}
	if match, ok := nearestSymbolName(node.Name, scope); ok {
		msg := "unknown identifier `" + node.Name + "`"
		d := diagnostics.NewError(msg).
			WithCode(diagnostics.ErrUndefinedSymbol).
			WithPrimaryLabel(ast.LocOf(node), msg).
			WithText("help", "did you mean `"+match+"`?", colors.GREEN)
		diag.Add(d)
		return false
	}
	msg := "unknown identifier `" + node.Name + "`"
	diag.Add(diagnostics.NewError(msg).WithCode(diagnostics.ErrUndefinedSymbol).WithPrimaryLabel(ast.LocOf(node), msg))
	return false
}

func nearestSymbolName(name string, scope *table.Scope) (string, bool) {
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
