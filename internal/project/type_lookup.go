package project

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
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

// CanonicalEnumDeclaration resolves a type qualifier through transparent
// aliases while retaining the original declaration-owned variant scope.
func CanonicalEnumDeclaration(ctx *CompilerContext, qualifier *symbols.Symbol) (*symbols.Symbol, bool) {
	if ctx == nil || qualifier == nil || qualifier.Kind != symbols.SymbolType {
		return nil, false
	}
	typ, ok := symbols.GetSymbolType(qualifier)
	if !ok {
		return nil, false
	}
	canonical, ok := typeinfo.Unalias(typ).(*typeinfo.DefinedType)
	if !ok || canonical == nil || canonical.Kind != typeinfo.DefinedKindEnum || canonical.Identity == "" {
		return nil, false
	}

	ctx.mu.RLock()
	owner := ctx.typeDeclarations[canonical.Identity]
	if owner == nil {
		instance, found := ctx.typeInstances[canonical.Identity]
		if found && instance.typ == canonical && instance.complete {
			owner = ctx.modules[instance.ownerModuleKey]
		}
	}
	ctx.mu.RUnlock()
	if owner == nil || owner.ModuleScope == nil {
		return nil, false
	}
	declaration, found := owner.ModuleScope.LookupLocal(canonical.Name)
	if !found || declaration == nil || declaration.Scope == nil {
		return nil, false
	}
	if _, enum := declaration.ASTNode.(*ast.EnumDecl); !enum {
		return nil, false
	}
	return declaration, true
}
