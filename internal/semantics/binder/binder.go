package binder

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/problems"
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
	ordered, completionCycle := b.typeDeclarationOrder()
	for _, decl := range ordered {
		b.bindTypeDecl(decl)
	}
	completed := make([]*typeinfo.DefinedType, 0, len(completionCycle))
	for _, decl := range completionCycle {
		if defined := b.bindTypeDecl(decl); defined != nil {
			completed = append(completed, defined)
		}
	}
	b.ctx.CompleteTypeInstances(completed)
	ast.ForEachDecl(b.module.AST, func(decl ast.Decl) bool {
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
	b.module.Bindings.SortOperationFunctions()
}

// Bind function and top-level declaration signatures into module scope.
func (b *binder) bindFunctionDecl(fn *ast.FnDecl) {
	if b == nil || b.module == nil || fn == nil || fn.Name == nil {
		return
	}
	fnType := project.ResolveFunctionType(b.ctx, b.module, fn, project.TypeContext{})
	sym := b.module.Bindings.Symbol(fn.Name)
	if fn.Receiver != nil {
		if sym == nil {
			return
		}
		sym.BindType(fnType)
		if len(fnType.Params) == 0 {
			return
		}
		if previous := b.module.Bindings.RegisterMethod(fnType.Params[0], sym); previous != nil {
			target, _ := typeinfo.ReceiverTarget(fnType.Params[0])
			message := "method `" + sym.Name + "` already declared for `" + typeinfo.TypeText(target) + "`"
			b.ctx.Diagnostics.Add(problems.Redeclaration(message, sym.Location, previous.Location))
		}
		return
	}
	if sym == nil {
		sym = b.moduleScopeSymbol(fn.Name.Name)
	}
	if sym != nil {
		sym.BindType(fnType)
		if len(fnType.Params) > 0 {
			b.module.Bindings.AddOperationFunction(sym)
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
		project.ResolveType(b.ctx, b.module, typ, project.TypeContext{}))
}

// Bind named type declarations using one stable shell per symbol.
// Recursive self-references must see same DefinedType object.
func (b *binder) bindTypeDecl(decl ast.TypeDecl) *typeinfo.DefinedType {
	if b == nil || b.module == nil || decl == nil {
		return nil
	}
	name := decl.DeclName()
	typ := decl.UnderlyingType()
	if name == nil || name.Name == "" {
		return nil
	}
	sym := b.moduleScopeSymbol(name.Name)
	if sym == nil {
		return nil
	}
	defined, ok := sym.Type.(*typeinfo.DefinedType)
	if ok && defined != nil {
		// Reuse same shell so self-references keep same type identity.
		defined.Name = name.Name
		defined.Identity = b.module.TypeDeclarationIdentity(name.Name)
	} else {
		defined = &typeinfo.DefinedType{
			Name:     name.Name,
			Identity: b.module.TypeDeclarationIdentity(name.Name),
		}
		sym.BindType(defined)
	}
	context := project.TypeContext{
		AllowAbstractSelf: true,
		TypeParameters:    typeinfo.TypeParameterBindings(defined.TypeParameters, nil),
	}
	if _, ok := decl.(*ast.InterfaceDecl); ok {
		context.NamedInterfaceRoot = typ
	}
	defined.Underlying = project.ResolveType(b.ctx, b.module, typ, context)
	return defined
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
