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
	imported, ok := ctx.ModuleByID(imp.ID)
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

// CanonicalEnumDeclaration resolves a semantic type through transparent
// aliases while retaining the qualifier's owner and declaration-owned variant
// scope. The separate owners preserve alias spelling without copying cases.
func CanonicalEnumDeclaration(ctx *CompilerContext, typ typeinfo.Type) (*Module, *symbols.Symbol, bool) {
	if ctx == nil || typ == nil {
		return nil, nil, false
	}
	qualified, ok := typ.(*typeinfo.DefinedType)
	if !ok || qualified == nil || qualified.Identity == "" {
		return nil, nil, false
	}
	canonical, ok := typeinfo.Unalias(typ).(*typeinfo.DefinedType)
	if !ok || canonical == nil || canonical.Kind != typeinfo.DefinedKindEnum || canonical.Identity == "" {
		return nil, nil, false
	}

	ctx.mu.RLock()
	ownerOf := func(defined *typeinfo.DefinedType) *Module {
		owner := ctx.typeDeclarations[defined.Identity]
		if owner == nil {
			instance, found := ctx.typeInstances[defined.Identity]
			if found && instance.typ == defined && instance.complete {
				owner = ctx.modules[instance.ownerModuleID]
			}
		}
		return owner
	}
	qualifierOwner := ownerOf(qualified)
	declarationOwner := ownerOf(canonical)
	ctx.mu.RUnlock()
	if qualifierOwner == nil || declarationOwner == nil || declarationOwner.ModuleScope == nil {
		return nil, nil, false
	}
	declaration, found := declarationOwner.ModuleScope.LookupLocal(canonical.Name)
	if !found || declaration == nil || declaration.Scope == nil {
		return nil, nil, false
	}
	if _, enum := declaration.ASTNode.(*ast.EnumDecl); !enum {
		return nil, nil, false
	}
	return qualifierOwner, declaration, true
}
