package typeresolution

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/types"
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
func (r *Resolver) CanonicalEnumDeclaration(typ types.Type) (*module.Module, *symbols.Symbol, bool) {
	if r == nil || typ == nil {
		return nil, nil, false
	}
	qualified, ok := typ.(*types.DefinedType)
	if !ok || qualified == nil || qualified.Identity == "" {
		return nil, nil, false
	}
	canonical, ok := types.Unalias(typ).(*types.DefinedType)
	if !ok || canonical == nil || canonical.Kind != types.DefinedKindEnum || canonical.Identity == "" {
		return nil, nil, false
	}

	qualifierOwner := r.typeOwner(qualified.Identity)
	declarationOwner := r.typeOwner(canonical.Identity)
	if qualifierOwner == nil || declarationOwner == nil || declarationOwner.ModuleScope == nil {
		return nil, nil, false
	}
	declaration, found := declarationOwner.ModuleScope.LookupLocal(canonical.Name)
	if !found || declaration == nil || declaration.Scope == nil {
		return nil, nil, false
	}
	if _, isEnum := declaration.ASTNode.(*ast.EnumDecl); !isEnum {
		return nil, nil, false
	}
	return qualifierOwner, declaration, true
}

// typeOwner returns the module that declares the type with this identity. A
// type identity names its declaring module, and a generic instance keeps that
// prefix, so no resolver index is needed.
func (r *Resolver) typeOwner(identity string) *module.Module {
	if r == nil || r.modules == nil {
		return nil
	}
	ownerID, _, ok := moduleid.ParseTypeIdentity(identity)
	if !ok {
		return nil
	}
	owner, _ := r.modules.ModuleByID(ownerID)
	return owner
}
