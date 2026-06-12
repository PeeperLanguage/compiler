package binder

import (
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
	for _, decl := range b.module.AST.Decls {
		if name, typ, ok := ast.DeclaredTypeExpr(decl); ok {
			b.bindTypeDecl(name, typ)
			continue
		}
		switch node := decl.(type) {
		case *ast.FnDecl:
			b.bindFunctionDecl(node)
		case *ast.LetDecl:
			b.bindModuleBinding(node.Name, node.Type)
		case *ast.ConstDecl:
			b.bindModuleBinding(node.Name, node.Type)
		case *ast.ImplDecl:
			b.bindImplDecl(node)
		}
	}
}

func (b *binder) bindFunctionDecl(fn *ast.FnDecl) {
	if b == nil || b.module == nil || fn == nil || fn.Name == nil {
		return
	}
	b.bindModuleScopeType(fn.Name.Name,
		typeinfo.FuncTypeFromDeclWithOptions(fn, project.TypeSyntaxOptions(b.ctx, b.module, nil, false)))
}

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
		typeinfo.ASTTypeWithOptions(typ, project.TypeSyntaxOptions(b.ctx, b.module, nil, false)))
}

func (b *binder) bindTypeDecl(name *ast.Ident, typ ast.TypeExpr) {
	if b == nil || b.module == nil || name == nil || name.Name == "" {
		return
	}
	b.bindModuleScopeType(name.Name, &typeinfo.DefinedType{
		Name:       name.Name,
		Underlying: typeinfo.ASTTypeWithOptions(typ, project.TypeSyntaxOptions(b.ctx, b.module, nil, true)),
	})
}

func (b *binder) bindImplDecl(decl *ast.ImplDecl) {
	if b == nil || b.module == nil || b.module.Semantics == nil || decl == nil || decl.Target == nil {
		return
	}
	selfType := typeinfo.ASTTypeWithOptions(decl.Target, project.TypeSyntaxOptions(b.ctx, b.module, nil, false))
	for _, method := range decl.Methods {
		if method == nil {
			continue
		}
		sym, ok := b.module.Semantics.MethodSymbol[method.ID()]
		if !ok || sym == nil {
			continue
		}
		sym.BindType(typeinfo.FuncTypeFromDeclWithOptions(method, project.TypeSyntaxOptions(b.ctx, b.module, selfType, false)))
	}
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
