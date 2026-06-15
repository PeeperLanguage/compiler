package collector

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

type collector struct {
	ctx    *project.CompilerContext
	module *project.Module
}

func (c *collector) collectModule(mod *ast.Module) {
	if c == nil || c.ctx == nil || c.module == nil || mod == nil {
		return
	}
	c.module.ModuleScope = table.New(c.ctx.GlobalScope)
	c.module.ResetSemanticData()
	for alias := range c.module.Imports {
		if alias == "" {
			continue
		}
		imp := c.module.Imports[alias]
		impSym := symbols.New(alias, symbols.SymbolImport, imp.Decl, ast.LocOf(imp.Decl))
		impSym.Type = &typeinfo.UnknownType{}
		if err := c.module.ModuleScope.Declare(impSym); err != nil {
			if c.ctx != nil && c.ctx.Diagnostics != nil {
				c.ctx.Diagnostics.Add(diagnostics.NewError(err.Error()).WithCode(diagnostics.ErrAmbiguousImport))
			}
		}
	}
	for _, decl := range mod.Decls {
		c.collectNode(decl)
	}
}

func (c *collector) collectNode(node ast.Node) {
	if decl, ok := node.(ast.Decl); ok {
		if name, typ, ok := ast.DeclaredTypeExpr(decl); ok {
			c.collectConcreteTypeDecl(name, typ, node)
			return
		}
	}
	switch n := node.(type) {
	case *ast.FnDecl:
		c.collectFnDecl(n)
	case *ast.ImplDecl:
		c.collectImplDecl(n)
	case *ast.LetDecl:
		c.collectModuleBinding(n.Name, symbols.SymbolVar, n.Type, n)
	case *ast.ConstDecl:
		c.collectModuleBinding(n.Name, symbols.SymbolConst, n.Type, n)
	default:
		return
	}
}

func (c *collector) collectFnDecl(fn *ast.FnDecl) {
	if c == nil || c.module == nil || fn == nil {
		return
	}
	if fn.Name == nil || fn.Name.Name == "" {
		c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "function name required", ast.LocOf(fn), "")
		return
	}
	kind := symbols.SymbolFunc
	if fn.Body == nil {
		kind = symbols.SymbolUnknown
	}
	sym := symbols.New(fn.Name.Name, kind, fn, ast.LocOf(fn.Name))
	if fn.Body != nil {
		sym.Scope = table.New(c.module.ModuleScope)
	}
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), ast.LocOf(fn), "")
		return
	}
}

func (c *collector) collectConcreteTypeDecl(name *ast.Ident, typ ast.TypeExpr, node ast.Node) {
	if c == nil || c.module == nil || node == nil {
		return
	}
	if name == nil || name.Name == "" {
		c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "type name required", ast.LocOf(node), "")
		return
	}
	sym := symbols.New(name.Name, symbols.SymbolType, node, ast.LocOf(name))
	sym.Type = &typeinfo.DefinedType{
		Name:       name.Name,
		Underlying: typeinfo.TypeFromSyntax(typ),
	}
	if sym.Type == nil {
		sym.Type = &typeinfo.InvalidType{}
	}
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), ast.LocOf(node), "")
		return
	}
}

func (c *collector) collectModuleBinding(name *ast.Ident, kind symbols.Kind, typ ast.TypeExpr, node ast.Node) {
	if c == nil || c.module == nil || name == nil || name.Name == "" {
		return
	}
	sym := symbols.New(name.Name, kind, node, ast.LocOf(name))
	sym.Type = typeinfo.TypeFromSyntax(typ)
	if sym.Type == nil {
		sym.Type = &typeinfo.UnknownType{}
	}
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), ast.LocOf(node), "")
	}
}

func (c *collector) collectImplDecl(decl *ast.ImplDecl) {
	if c == nil || c.module == nil || c.module.Semantics == nil || decl == nil || decl.Target == nil {
		return
	}
	targetKey := typeinfo.TypeText(typeinfo.TypeFromSyntax(decl.Target))
	for _, method := range decl.Methods {
		if method == nil || method.Name == nil || method.Name.Name == "" {
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "method name required", ast.LocOf(decl), "")
			continue
		}
		existing := c.module.Semantics.MethodSets[targetKey]
		duplicate := false
		for _, item := range existing {
			if item != nil && item.Name == method.Name.Name {
				duplicate = true
				break
			}
		}
		if duplicate {
			c.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, "method `"+method.Name.Name+"` already declared for `"+targetKey+"`", ast.LocOf(method), "")
			continue
		}
		sym := symbols.New(method.Name.Name, symbols.SymbolMethod, method, ast.LocOf(method.Name))
		sym.Scope = table.New(c.module.ModuleScope)
		c.module.Semantics.MethodSets[targetKey] = append(c.module.Semantics.MethodSets[targetKey], sym)
		c.module.Semantics.MethodSymbol[method.ID()] = sym
	}
}

func Collect(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || module == nil || module.AST == nil {
		return
	}
	c := &collector{ctx: ctx, module: module}
	c.collectModule(module.AST)
}
