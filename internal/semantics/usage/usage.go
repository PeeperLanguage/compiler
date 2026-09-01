package usage

import (
	"fmt"

	"compiler/internal/diagnostics"
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
			// Only the exact discard binding `_` suppresses unused warnings.
			if !symbols.IsPubName(sym.Name) && !sym.Used && sym.Name != "_" {
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
	if module.Bindings != nil {
		for _, scope := range module.Bindings.BlockScopes {
			if scope == nil {
				continue
			}
			for _, sym := range scope.Symbols() {
				if sym.Name == "_" {
					continue
				}
				if !sym.Used {
					switch sym.Kind {
					case symbols.SymbolParam:
						name := "parameter"
						if sym.IsReceiver {
							name = "receiver"
						}
						ctx.Diagnostics.AddWarning(diagnostics.WarnUnusedParameter,
							fmt.Sprintf("unused %s `%s`", name, sym.Name), sym.Location, "use it or rename it to `_` to suppress warning")
					case symbols.SymbolVar, symbols.SymbolConst:
						ctx.Diagnostics.AddWarning(diagnostics.WarnUnusedLocal,
							fmt.Sprintf("unused local `%s`", sym.Name), sym.Location, "use it or rename it to `_` to suppress warning")
					}
					continue
				}
				if !sym.IsMutable() || sym.RequiresMutable || sym.MutableLocation == nil {
					continue
				}
				ctx.Diagnostics.AddWarning(diagnostics.WarnUnmodifiedMutable,
					fmt.Sprintf("mutable binding `%s` is never modified", sym.Name), sym.MutableLocation, "remove unnecessary `mut`").
					WithCodeReplacement(sym.MutableLocation, "mut", "").
					WithHelp("remove unnecessary `mut`")
			}
		}
	}
}
