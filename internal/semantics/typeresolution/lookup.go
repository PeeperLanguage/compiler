package typeresolution

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// ImportedSymbol bundles import, module, and symbol so consumers do not repeat
// alias and module-registry traversal.
type ImportedSymbol struct {
	Import module.ResolvedImport
	Module *module.Module
	Symbol *symbols.Symbol
}

func (r *Resolver) LookupImportedSymbol(current *module.Module, alias, symbolName string) (ImportedSymbol, bool) {
	out := ImportedSymbol{}
	if r == nil || r.modules == nil || current == nil || current.ModuleScope == nil || alias == "" || symbolName == "" {
		return out, false
	}
	imp, ok := current.Imports[alias]
	if !ok {
		return out, false
	}
	out.Import = imp
	imported, ok := r.modules.ModuleByID(imp.ID)
	if !ok || imported == nil || imported.ModuleScope == nil {
		return out, false
	}
	out.Module = imported
	sym, found := imported.ModuleScope.LookupLocal(symbolName)
	if !found || sym == nil {
		return out, false
	}
	out.Symbol = sym
	return out, true
}

// CanonicalEnumDeclaration resolves transparent aliases while retaining both
// qualifier ownership and the declaration-owned variant scope.
func (r *Resolver) CanonicalEnumDeclaration(typ typeinfo.Type) (*module.Module, *symbols.Symbol, bool) {
	if r == nil || typ == nil {
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

	qualifierOwner := r.typeOwner(qualified)
	declarationOwner := r.typeOwner(canonical)
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

// typeOwner reads resolver state before consulting project module identity.
// Keeping lock acquisition in this order lets project registration update its
// module registry and derived resolver index atomically without lock inversion.
func (r *Resolver) typeOwner(defined *typeinfo.DefinedType) *module.Module {
	if r == nil || defined == nil {
		return nil
	}
	r.mu.RLock()
	owner := r.declarations[defined.Identity]
	instance, found := r.instances[defined.Identity]
	r.mu.RUnlock()
	if owner != nil {
		return owner
	}
	if !found || instance.typ != defined || !instance.complete || r.modules == nil {
		return nil
	}
	owner, _ = r.modules.ModuleByID(instance.ownerModuleID)
	return owner
}
