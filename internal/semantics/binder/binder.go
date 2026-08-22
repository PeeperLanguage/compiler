package binder

import (
	"cmp"
	"slices"

	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type binder struct {
	ctx    *project.CompilerContext
	module *project.Module
}

func Bind(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || module == nil || module.AST == nil || module.ModuleScope == nil {
		return
	}
	b := &binder{ctx: ctx, module: module}
	b.bindModule()
}

func (b *binder) bindModule() {
	ast.ForEachDecl(b.module.AST, func(decl ast.Decl) bool {
		if typeDecl, ok := decl.(ast.TypeDecl); ok {
			b.bindTypeDecl(typeDecl)
			return true
		}
		switch node := decl.(type) {
		case *ast.FnDecl:
			b.bindFunctionDecl(node)
		case *ast.LetDecl:
			b.bindModuleBinding(node.Name, node.Type)
		case *ast.ConstDecl:
			b.bindModuleBinding(node.Name, node.Type)
		}
		return true
	})
	slices.SortFunc(b.module.Semantics.OperationFunctions, func(left, right *symbols.Symbol) int {
		return cmp.Compare(left.Name, right.Name)
	})
	b.validateTypeDeclCycles()
}

// Bind function and top-level declaration signatures into module scope.
func (b *binder) bindFunctionDecl(fn *ast.FnDecl) {
	if b == nil || b.module == nil || fn == nil || fn.Name == nil {
		return
	}
	fnType := typeinfo.FuncTypeFromDeclWithOptions(fn, project.TypeSyntaxOptions(b.ctx, b.module, nil, false))
	if fn.Receiver != nil {
		if sym := b.module.Semantics.MethodSymbol[fn.ID()]; sym != nil {
			sym.BindType(fnType)
		}
		return
	}
	if sym := b.moduleScopeSymbol(fn.Name.Name); sym != nil {
		sym.BindType(fnType)
		if len(fnType.Params) > 0 {
			b.module.Semantics.OperationFunctions = append(b.module.Semantics.OperationFunctions, sym)
		}
	}
}

// Bind top-level value declarations. Explicit types win; otherwise keep
// placeholder type until later phase fills it.
func (b *binder) bindModuleBinding(name *ast.Ident, typ ast.TypeExpr) {
	if b == nil || b.module == nil || name == nil || name.Name == "" {
		return
	}
	if typ == nil {
		if b.moduleScopeSymbol(name.Name) == nil {
			return
		}
		b.bindModuleScopeTypeIfUnset(name.Name, &typeinfo.UnknownType{})
		return
	}
	b.bindModuleScopeType(name.Name,
		typeinfo.TypeFromSyntax(typ, project.TypeSyntaxOptions(b.ctx, b.module, nil, false)))
}

// Bind named type declarations using one stable shell per symbol.
// Recursive self-references must see same DefinedType object.
func (b *binder) bindTypeDecl(decl ast.TypeDecl) {
	if b == nil || b.module == nil || decl == nil {
		return
	}
	name := decl.DeclName()
	typ := decl.UnderlyingType()
	if name == nil || name.Name == "" {
		return
	}
	sym := b.moduleScopeSymbol(name.Name)
	if sym == nil {
		return
	}
	underlying := typeinfo.TypeFromSyntax(typ, project.TypeSyntaxOptions(b.ctx, b.module, nil, true))
	if defined, ok := sym.Type.(*typeinfo.DefinedType); ok && defined != nil {
		// Reuse same shell so self-references keep same type identity.
		defined.Name = name.Name
		defined.Underlying = underlying
	} else {
		sym.BindType(&typeinfo.DefinedType{
			Name:       name.Name,
			Underlying: underlying,
		})
	}
	b.registerTypeDecl(name.Name, typ)
}

func (b *binder) moduleScopeSymbol(name string) *symbols.Symbol {
	if b == nil || b.module == nil || b.module.ModuleScope == nil || name == "" {
		return nil
	}
	sym, ok := b.module.ModuleScope.LookupLocal(name)
	if !ok {
		return nil
	}
	return sym
}

func (b *binder) bindModuleScopeType(name string, typ typeinfo.Type) {
	if sym := b.moduleScopeSymbol(name); sym != nil && typ != nil {
		sym.BindType(typ)
	}
}

func (b *binder) bindModuleScopeTypeIfUnset(name string, typ typeinfo.Type) {
	if sym := b.moduleScopeSymbol(name); sym != nil && typ != nil && sym.Type == nil {
		sym.BindType(typ)
	}
}
