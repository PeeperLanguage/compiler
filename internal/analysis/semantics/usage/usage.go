package usage

import (
	"fmt"
	"strings"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/context"
)

func Analyze(ctx *context.CompilerContext, module *context.Module) {
	if ctx == nil || module == nil || module.ModuleScope == nil {
		return
	}

	// 1. Check for unused imports in ModuleScope
	for _, sym := range module.ModuleScope.Symbols() {
		if sym.Kind == symbols.SymbolImport {
			if !sym.Used {
				addWarning(ctx.Diagnostics, module.FilePath, sym, diagnostics.WarnUnusedImport,
					fmt.Sprintf("unused import `%s`", sym.Name))
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
				addWarning(ctx.Diagnostics, module.FilePath, sym, code, msg)
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
				if sym.Used || strings.HasPrefix(sym.Name, "_") {
					continue
				}
				switch sym.Kind {
				case symbols.SymbolParam:
					addWarning(ctx.Diagnostics, module.FilePath, sym, diagnostics.WarnUnusedParameter,
						fmt.Sprintf("unused parameter `%s`", sym.Name))
				case symbols.SymbolVar, symbols.SymbolConst:
					addWarning(ctx.Diagnostics, module.FilePath, sym, diagnostics.WarnUnusedLocal,
						fmt.Sprintf("unused local `%s`", sym.Name))
				}
			}
		}
	}
}

func addWarning(diag *diagnostics.DiagnosticBag, filePath string, sym *symbols.Symbol, code, msg string) {
	if diag == nil || sym == nil {
		return
	}
	d := diagnostics.NewWarning(msg).WithCode(code)
	if sym.Location != nil {
		d.WithPrimaryLabel(sym.Location, msg)
	}
	diag.Add(d)
}
