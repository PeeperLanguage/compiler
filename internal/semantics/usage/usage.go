package usage

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
)

func Analyze(diag *diagnostics.DiagnosticBag, module *module.Module, preludeID moduleid.ID) {
	if diag == nil || module == nil || module.ModuleScope == nil {
		return
	}

	// 1. Check for unused imports in ModuleScope
	for _, sym := range module.ModuleScope.Symbols() {
		if sym.Kind == symbols.SymbolImport {
			if !sym.IsUsed() {
				diag.AddWarning(diagnostics.WarnUnusedImport,
					fmt.Sprintf("unused import `%s`", sym.Name), sym.Location, "")
			}
		}
	}

	// 2. Check for unused private module-level symbols (functions, types, constants, variables)
	// Do not warn about prelude/global symbols since they represent a library
	if module.ID != preludeID {
		for _, sym := range module.ModuleScope.Symbols() {
			if sym.Kind == symbols.SymbolImport {
				continue
			}
			if sym.Name == "main" {
				continue
			}
			// Only the exact discard binding `_` suppresses unused warnings.
			if !symbols.IsPubName(sym.Name) && !sym.IsUsed() && sym.Name != "_" {
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
				diag.AddWarning(code, msg, sym.Location, "")
			}
		}
	}

	// 3. Check for unused local variables and parameters
	if module.Bindings != nil {
		module.Bindings.ForEachScope(func(scope *symbols.Scope) {
			for _, sym := range scope.Symbols() {
				if sym.Name == "_" {
					continue
				}
				if !sym.IsUsed() {
					switch sym.Kind {
					case symbols.SymbolParam:
						name := "parameter"
						if sym.IsReceiver {
							name = "receiver"
						}
						diag.AddWarning(diagnostics.WarnUnusedParameter,
							fmt.Sprintf("unused %s `%s`", name, sym.Name), sym.Location, "use it or rename it to `_` to suppress warning")
					case symbols.SymbolVar, symbols.SymbolConst:
						diag.AddWarning(diagnostics.WarnUnusedLocal,
							fmt.Sprintf("unused local `%s`", sym.Name), sym.Location, "use it or rename it to `_` to suppress warning")
					}
					continue
				}
				if !sym.IsMutable() || sym.RequiresMutable() || sym.MutableLocation == nil {
					continue
				}
				diag.AddWarning(diagnostics.WarnUnmodifiedMutable,
					fmt.Sprintf("mutable binding `%s` is never modified", sym.Name), sym.MutableLocation, "remove unnecessary `mut`").
					WithCodeReplacement(sym.MutableLocation, "mut", "").
					WithHelp("remove unnecessary `mut`")
			}
		})
	}
}
