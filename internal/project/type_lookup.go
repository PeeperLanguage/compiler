package project

import (
	"compiler/internal/semantics/symbols"
)

// ImportedSymbolLookup bundles import/module/symbol so one foreign lookup can
// serve resolver, checker, and lowerer without repeating import traversal.
type ImportedSymbolLookup struct {
	Import ResolvedImport
	Module *Module
	Symbol *symbols.Symbol
}

// LookupImportedSymbol walks alias -> import -> module -> symbol once.
// Resolver uses the symbol for export checks, checker uses it for type lookup,
// and lowerer uses it for stable IR naming. One kernel keeps those phases in sync.
func LookupImportedSymbol(ctx *CompilerContext, currentModule *Module, importedModule, symbolName string) (ImportedSymbolLookup, bool) {
	out := ImportedSymbolLookup{}
	if ctx == nil || currentModule == nil || currentModule.ModuleScope == nil || importedModule == "" || symbolName == "" {
		return out, false
	}
	imp, ok := currentModule.Imports[importedModule]
	if !ok {
		return out, false
	}
	out.Import = imp
	if impSym, ok := currentModule.ModuleScope.LookupLocal(importedModule); ok && impSym != nil {
		impSym.Used = true
	}
	imported, ok := ctx.ModuleByKey(imp.Key)
	if !ok || imported == nil || imported.ModuleScope == nil {
		return out, false
	}
	out.Module = imported
	sym, found := imported.ModuleScope.LookupLocal(symbolName)
	if !found || sym == nil {
		return out, false
	}
	sym.Used = true
	out.Symbol = sym
	return out, true
}
