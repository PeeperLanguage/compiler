package project

import (
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func LookupTypeInCurrentModule(module *Module, symbolName string) (foundType typeinfo.Type, hasFound bool) {
	if module == nil || module.ModuleScope == nil || symbolName == "" {
		return nil, false
	}
	sym, found := module.ModuleScope.Lookup(symbolName)
	if !found || sym == nil || sym.Kind != symbols.SymbolType {
		return nil, false
	}
	sym.Used = true
	resolved, ok := symbols.GetSymbolType(sym)
	return resolved, ok
}

func LookupTypeInImportedModule(ctx *CompilerContext, currentModule *Module, importedModule, symbolName string) (foundType typeinfo.Type, hasFound bool) {
	if ctx == nil || currentModule == nil || currentModule.ModuleScope == nil || importedModule == "" || symbolName == "" {
		return nil, false
	}
	imp, ok := currentModule.Imports[importedModule]
	if !ok {
		return nil, false
	}
	if impSym, ok := currentModule.ModuleScope.LookupLocal(importedModule); ok && impSym != nil {
		impSym.Used = true
	}
	imported, ok := ctx.ModuleByKey(imp.Key)
	if !ok || imported == nil || imported.ModuleScope == nil {
		return nil, false
	}
	sym, found := imported.ModuleScope.LookupLocal(symbolName)
	if !found || sym == nil || sym.Kind != symbols.SymbolType {
		return nil, false
	}
	sym.Used = true
	resolved, ok := symbols.GetSymbolType(sym)
	return resolved, ok
}
