package collector

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/problems"
	"compiler/internal/project"

	"compiler/internal/semantics/symbols"
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
	c.module.ModuleScope = symbols.NewScope(c.ctx.GlobalScope)
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
	for _, stmt := range mod.Stmts {
		c.collectNode(stmt)
	}
}

func (c *collector) collectNode(node ast.Node) {
	if decl, ok := node.(ast.TypeDecl); ok {
		if name := decl.DeclName(); name != nil {
			typ := decl.UnderlyingType()
			c.collectConcreteTypeDecl(name, typ, node)
			return
		}
	}
	switch n := node.(type) {
	case *ast.FnDecl:
		c.collectFnDecl(n)
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
	if fn.Receiver != nil {
		receiverType := typeinfo.TypeFromSyntax(fn.Receiver.Type, typeinfo.SyntaxOptions{Target: c.ctx.Target})
		targetType, ok := typeinfo.ReceiverTarget(receiverType)
		if !ok {
			return
		}
		targetKey := typeinfo.TypeText(targetType)
		var previous *symbols.Symbol
		for _, item := range c.module.Semantics.MethodSets[targetKey] {
			if item != nil && item.Name == fn.Name.Name {
				previous = item
				break
			}
		}
		if previous != nil {
			message := "method `" + fn.Name.Name + "` already declared for `" + targetKey + "`"
			c.ctx.Diagnostics.Add(problems.Redeclaration(message, fn.Name.Location, previous.Location))
			return
		}
		sym := symbols.New(fn.Name.Name, symbols.SymbolMethod, fn, ast.LocOf(fn.Name))
		sym.DefiningModule = c.module.DefiningModuleKey()
		sym.Scope = symbols.NewScope(c.module.ModuleScope)
		c.module.Semantics.MethodSets[targetKey] = append(c.module.Semantics.MethodSets[targetKey], sym)
		c.module.Semantics.MethodSymbol[fn.ID()] = sym
		return
	}
	sym := symbols.New(fn.Name.Name, symbols.SymbolFunc, fn, ast.LocOf(fn.Name))
	sym.DefiningModule = c.module.DefiningModuleKey()
	sym.Scope = symbols.NewScope(c.module.ModuleScope)
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		problems.ReportRedeclaration(c.ctx.Diagnostics, c.module.ModuleScope, err.Error(), fn.Name.Name, fn.Name.Location)
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
		Name: name.Name,
		// Underlying is filled by binder.
	}
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		problems.ReportRedeclaration(c.ctx.Diagnostics, c.module.ModuleScope, err.Error(), name.Name, name.Location)
		return
	}
}

func (c *collector) collectModuleBinding(name *ast.Ident, kind symbols.Kind, typ ast.TypeExpr, node ast.Node) {
	if c == nil || c.module == nil || name == nil || name.Name == "" {
		return
	}
	sym := symbols.New(name.Name, kind, node, ast.LocOf(name))
	sym.Type = &typeinfo.UnknownType{} // binder fills real type
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		problems.ReportRedeclaration(c.ctx.Diagnostics, c.module.ModuleScope, err.Error(), name.Name, name.Location)
	}
}

func Collect(ctx *project.CompilerContext, module *project.Module) {
	if ctx == nil || module == nil || module.AST == nil {
		return
	}
	c := &collector{ctx: ctx, module: module}
	c.collectModule(module.AST)
}
