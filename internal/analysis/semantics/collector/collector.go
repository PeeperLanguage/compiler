package collector

import (
	"compiler/pkg/diagnostics"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
)

type collector struct {
	ctx    *context.CompilerContext
	module *context.Module
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
		impSym := symbols.New(alias, symbols.SymbolImport, imp.Decl)
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
	switch n := node.(type) {
	case *ast.FnDecl:
		c.collectFnDecl(n)
	case *ast.TypeAliasDecl:
		c.collectTypeAliasDecl(n)
	case *ast.StructDecl:
		c.collectConcreteTypeDecl(n.Name, &ast.StructType{Fields: n.Fields, Location: n.Location}, n)
	case *ast.InterfaceDecl:
		c.collectConcreteTypeDecl(n.Name, &ast.InterfaceType{Methods: n.Methods, Location: n.Location}, n)
	case *ast.EnumDecl:
		c.collectConcreteTypeDecl(n.Name, &ast.EnumType{Variants: n.Variants, Location: n.Location}, n)
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
		c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "function name required", fn.Loc(), "")
		return
	}
	kind := symbols.SymbolFunc
	if fn.Body == nil {
		kind = symbols.SymbolUnknown
	}
	sym := symbols.New(fn.Name.Name, kind, fn)
	if fn.Body != nil {
		sym.Scope = table.New(c.module.ModuleScope)
	}
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), fn.Loc(), "")
		return
	}
}

func (c *collector) collectTypeAliasDecl(decl *ast.TypeAliasDecl) {
	c.collectConcreteTypeDecl(decl.Name, decl.Type, decl)
}

func (c *collector) collectConcreteTypeDecl(name *ast.Ident, typ ast.TypeExpr, node ast.Node) {
	if c == nil || c.module == nil || node == nil {
		return
	}
	if name == nil || name.Name == "" {
		c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "type name required", node.Loc(), "")
		return
	}
	sym := symbols.New(name.Name, symbols.SymbolType, node)
	sym.Type = &typeinfo.DefinedType{
		Name:       name.Name,
		Underlying: typeinfo.TypeFromSyntax(typ),
	}
	if sym.Type == nil {
		sym.Type = &typeinfo.InvalidType{}
	}
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), node.Loc(), "")
		return
	}
}

func (c *collector) collectModuleBinding(name *ast.Ident, kind symbols.Kind, typ ast.TypeExpr, node ast.Node) {
	if c == nil || c.module == nil || name == nil || name.Name == "" {
		return
	}
	sym := symbols.New(name.Name, kind, node)
	sym.Type = typeinfo.TypeFromSyntax(typ)
	if sym.Type == nil {
		sym.Type = &typeinfo.UnknownType{}
	}
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, err.Error(), node.Loc(), "")
	}
}

func (c *collector) collectImplDecl(decl *ast.ImplDecl) {
	if c == nil || c.module == nil || c.module.Semantics == nil || decl == nil || decl.Target == nil {
		return
	}
	targetKey := typeinfo.TypeText(typeinfo.TypeFromSyntax(decl.Target))
	for _, method := range decl.Methods {
		if method == nil || method.Name == nil || method.Name.Name == "" {
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "method name required", decl.Loc(), "")
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
			c.ctx.Diagnostics.AddError(diagnostics.ErrRedeclaredSymbol, "method `"+method.Name.Name+"` already declared for `"+targetKey+"`", method.Loc(), "")
			continue
		}
		sym := symbols.New(method.Name.Name, symbols.SymbolMethod, method)
		sym.Scope = table.New(c.module.ModuleScope)
		c.module.Semantics.MethodSets[targetKey] = append(c.module.Semantics.MethodSets[targetKey], sym)
		c.module.Semantics.MethodSymbol[method.ID()] = sym
	}
}

func Collect(ctx *context.CompilerContext, module *context.Module) {
	if ctx == nil || module == nil || module.AST == nil {
		return
	}
	c := &collector{ctx: ctx, module: module}
	c.collectModule(module.AST)
}
