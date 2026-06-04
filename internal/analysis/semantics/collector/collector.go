package collector

import (
	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
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
		impSym := symbols.New(alias, symbols.SymbolImport, nil)
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
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, fn, diagnostics.ErrMissingIdentifier, "function name required")
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
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, fn, diagnostics.ErrRedeclaredSymbol, err.Error())
		return
	}
}

func (c *collector) collectTypeAliasDecl(decl *ast.TypeAliasDecl) {
	if c == nil || c.module == nil || decl == nil {
		return
	}
	if decl.Name == nil || decl.Name.Name == "" {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, decl, diagnostics.ErrMissingIdentifier, "type name required")
		return
	}
	sym := symbols.New(decl.Name.Name, symbols.SymbolType, decl)
	sym.Type = typeinfo.TypeFromSyntax(decl.Type)
	if sym.Type == nil {
		sym.Type = &typeinfo.InvalidType{}
	}
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, decl, diagnostics.ErrRedeclaredSymbol, err.Error())
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
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrRedeclaredSymbol, err.Error())
	}
}

func Collect(ctx *context.CompilerContext, module *context.Module) {
	if ctx == nil || module == nil || module.AST == nil {
		return
	}
	c := &collector{ctx: ctx, module: module}
	c.collectModule(module.AST)
}
