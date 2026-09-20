package collector

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/problems"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type collector struct {
	ctx    *project.CompilerContext
	module *module.Module
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
		impSym.BindType(&typeinfo.UnknownType{})
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
			c.collectConcreteTypeDecl(decl)
			return
		}
	}
	switch n := node.(type) {
	case *ast.FnDecl:
		c.collectFnDecl(n)
	case *ast.LetDecl:
		c.collectModuleBinding(n.Name, symbols.SymbolVar, n)
	case *ast.ConstDecl:
		c.collectModuleBinding(n.Name, symbols.SymbolConst, n)
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
	if fn.Receiver != nil {
		kind = symbols.SymbolMethod
	}
	sym := symbols.New(fn.Name.Name, kind, fn, ast.LocOf(fn.Name))
	sym.DefiningModule = c.module.ID
	sym.Scope = symbols.NewScope(c.module.ModuleScope)
	c.module.Bindings.Bind(fn.Name, sym)
	if fn.Receiver != nil {
		// Receiver ownership is registered after binding resolves the semantic
		// receiver type. Collection only publishes declaration identity.
		return
	}
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		problems.ReportRedeclaration(c.ctx.Diagnostics, c.module.ModuleScope, err.Error(), fn.Name.Name, fn.Name.Location)
		return
	}
}
func (c *collector) collectConcreteTypeDecl(decl ast.TypeDecl) {
	if c == nil || c.module == nil || decl == nil {
		return
	}
	name := decl.DeclName()
	if name == nil || name.Name == "" {
		c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "type name required", ast.LocOf(decl), "")
		return
	}
	identity := c.module.TypeDeclarationIdentity(name.Name)
	parameters := make([]*typeinfo.TypeParameterType, 0, len(decl.DeclarationTypeParams()))
	for index, parameter := range decl.DeclarationTypeParams() {
		if parameter.Name != nil && parameter.Name.Name != "" {
			parameters = append(parameters, &typeinfo.TypeParameterType{
				Name: parameter.Name.Name, OwnerIdentity: identity, Index: index,
			})
		}
	}
	kind := typeinfo.DefinedKindInvalid
	switch decl.(type) {
	case *ast.TypeAliasDecl:
		kind = typeinfo.DefinedKindAlias
	case *ast.StructDecl:
		kind = typeinfo.DefinedKindStruct
	case *ast.InterfaceDecl:
		kind = typeinfo.DefinedKindInterface
	case *ast.EnumDecl:
		kind = typeinfo.DefinedKindEnum
	}
	sym := symbols.New(name.Name, symbols.SymbolType, decl, ast.LocOf(name))
	defined := &typeinfo.DefinedType{
		Name:           name.Name,
		Identity:       identity,
		Kind:           kind,
		TypeParameters: parameters,
		// Underlying is filled by binder.
	}
	sym.BindType(defined)
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		problems.ReportRedeclaration(c.ctx.Diagnostics, c.module.ModuleScope, err.Error(), name.Name, name.Location)
		return
	}
	c.module.Bindings.Bind(name, sym)
	if enumDecl, ok := decl.(*ast.EnumDecl); ok {
		sym.Scope = symbols.NewScope(nil)
		if enumType, ok := enumDecl.Type.(*ast.EnumType); ok && enumType != nil {
			for _, variant := range enumType.Variants {
				if variant.Name == nil || variant.Name.Name == "" {
					continue
				}
				variantSymbol := symbols.New(variant.Name.Name, symbols.SymbolVariant, variant.Name, variant.Name.Location)
				variantSymbol.BindType(defined)
				variantSymbol.DefiningModule = c.module.ID
				if err := sym.Scope.Declare(variantSymbol); err != nil {
					problems.ReportRedeclaration(c.ctx.Diagnostics, sym.Scope, err.Error(), variant.Name.Name, variant.Name.Location)
					continue
				}
				c.module.Bindings.Bind(variant.Name, variantSymbol)
			}
		}
	}
	c.ctx.TypeResolver.RegisterTypeDeclaration(c.module, decl, defined)
}

func (c *collector) collectModuleBinding(name *ast.Ident, kind symbols.Kind, node ast.Node) {
	if c == nil || c.module == nil || name == nil || name.Name == "" {
		return
	}
	sym := symbols.New(name.Name, kind, node, ast.LocOf(name))
	sym.DefiningModule = c.module.ID
	sym.BindType(&typeinfo.UnknownType{}) // binder fills real type
	if err := c.module.ModuleScope.Declare(sym); err != nil {
		problems.ReportRedeclaration(c.ctx.Diagnostics, c.module.ModuleScope, err.Error(), name.Name, name.Location)
		return
	}
	c.module.Bindings.Bind(name, sym)
}

func Collect(ctx *project.CompilerContext, module *module.Module) {
	if ctx == nil || module == nil || module.AST == nil {
		return
	}
	c := &collector{ctx: ctx, module: module}
	c.collectModule(module.AST)
}
