package usage

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
)

func Analyze(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || module == nil || module.ModuleScope == nil {
		return
	}

	// 1. Check for unused imports in ModuleScope
	for _, sym := range module.ModuleScope.Symbols() {
		if sym.Kind == symbols.SymbolImport {
			if !sym.Used {
				ctx.Diagnostics.AddWarning(diagnostics.WarnUnusedImport,
					fmt.Sprintf("unused import `%s`", sym.Name), sym.Location, "")
			}
		}
	}

	// 2. Check for unused private module-level symbols (functions, types, constants, variables)
	// Do not warn about prelude/global symbols since they represent a library
	if module.Key != "core:prelude/global" {
		for _, sym := range module.ModuleScope.Symbols() {
			if sym.Kind == symbols.SymbolImport {
				continue
			}
			if sym.Name == "main" {
				continue
			}
			// Only check private symbols (which are not exported and don't start with "_")
			if !symbols.IsPubName(sym.Name) && !sym.Used && !strings.HasPrefix(sym.Name, "_") {
				var code string
				var msg string
				switch sym.Kind {
				case symbols.SymbolFunc:
					code = diagnostics.WarnUnusedPrivateFunction
					msg = fmt.Sprintf("unused private function `%s`", sym.Name)
				case symbols.SymbolType:
					code = diagnostics.WarnUnusedPrivateType
					msg = fmt.Sprintf("unused private type `%s`", sym.Name)
				case symbols.SymbolVar, symbols.SymbolConst:
					code = diagnostics.WarnUnusedPrivateBinding
					msg = fmt.Sprintf("unused private binding `%s`", sym.Name)
				default:
					continue
				}
				ctx.Diagnostics.AddWarning(code, msg, sym.Location, "")
			}
		}
	}

	// 3. Check for unused local variables and parameters
	if module.Semantics != nil {
		for _, scope := range module.Semantics.BlockScopes {
			if scope == nil {
				continue
			}
			for _, sym := range scope.Symbols() {
				if shouldDiscardBindingValue(sym) {
					module.Semantics.DiscardBindingValue[sym.ID] = struct{}{}
				}
				if sym.Used || strings.HasPrefix(sym.Name, "_") {
					continue
				}
				switch sym.Kind {
				case symbols.SymbolParam:
					ctx.Diagnostics.AddWarning(diagnostics.WarnUnusedParameter,
						fmt.Sprintf("unused parameter `%s`", sym.Name), sym.Location, "use it or add `_` prefix to suppress warning")
				case symbols.SymbolVar, symbols.SymbolConst:
					ctx.Diagnostics.AddWarning(diagnostics.WarnUnusedLocal,
						fmt.Sprintf("unused local `%s`", sym.Name), sym.Location, "use it or add `_` prefix to suppress warning")
				}
			}
		}
	}
}

func shouldDiscardBindingValue(sym *symbols.Symbol) bool {
	if sym == nil || sym.Used {
		return false
	}
	switch node := sym.ASTNode.(type) {
	case *ast.LetDecl:
		return sym.Kind == symbols.SymbolVar && node != nil && isDiscardableBindingValue(node.Value)
	case *ast.ConstDecl:
		return sym.Kind == symbols.SymbolConst && node != nil && isDiscardableBindingValue(node.Value)
	default:
		return false
	}
}

func isDiscardableBindingValue(expr ast.Expr) bool {
	_, ok := expr.(*ast.CallExpr)
	return ok
}
