package typechecker

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type checker struct {
	ctx                 *project.CompilerContext
	module              *project.Module
	flow                *flowCheck
	siteOnly            bool
	payloadContext      int
	optionalTestContext int
	wholeCarrierExpr    ast.Expr
	loopDepth           int
}

// Concrete references convert to satisfied interface borrows, while owned
// concrete pointers erase by adopting their existing allocation.
const allowImplicitInterfaceConversion = true

// enclosingFnDecl walks up the scope chain and returns the FnDecl of the
// enclosing function, or nil if not inside a function body.
func (c *checker) enclosingFnDecl(scope *symbols.Scope) *ast.FnDecl {
	if c == nil || c.module == nil || c.module.ModuleScope == nil {
		return nil
	}
	for s := scope; s != nil && s != c.module.ModuleScope; s = s.Parent() {
		for _, sym := range c.module.ModuleScope.Symbols() {
			if (sym.Kind == symbols.SymbolFunc || sym.Kind == symbols.SymbolMethod) && sym.Scope == s {
				if fn, ok := sym.ASTNode.(*ast.FnDecl); ok {
					return fn
				}
			}
		}
	}
	return nil
}

func (c *checker) requireValueType(expr ast.Expr, typ typeinfo.Type, context string) typeinfo.Type {
	if typ != nil {
		return typ
	}
	if c != nil && c.ctx != nil {
		c.ctx.Diagnostics.Add(invalidExpressionError(expr, context+" requires a value-producing expression"))
	}
	return &typeinfo.InvalidType{}
}

func (c *checker) expandedDefaultBinding(ident *ast.Ident) (place.Binding, bool) {
	if c == nil || c.module == nil || c.module.Semantics == nil || ident == nil {
		return place.Binding{}, false
	}
	if _, ok := c.module.Semantics.ExpandedDefaultBindings[ident.ID()]; !ok {
		return place.Binding{}, false
	}
	return place.Binding{Symbol: c.module.Semantics.ResolvedSymbols[ident.ID()]}, true
}

func (c *checker) checkModule() {
	if c == nil || c.module == nil || c.module.AST == nil {
		return
	}
	c.checkFunctionTypeContracts()
	ast.ForEachDecl(c.module.AST, func(decl ast.Decl) bool {
		c.checkDeclAttributes(decl)
		typeDecl, ok := decl.(ast.TypeDecl)
		if !ok {
			return true
		}
		if iface, ok := typeDecl.(*ast.InterfaceDecl); ok {
			c.checkInterfaceDecl(iface)
		}
		if enum, ok := typeDecl.(*ast.EnumDecl); ok {
			c.checkEnumDecl(enum)
		}
		c.checkTypeDeclReferenceStorage(typeDecl)
		return true
	})
	ast.ForEachDecl(c.module.AST, func(decl ast.Decl) bool {
		switch node := decl.(type) {
		case *ast.LetDecl:
			if c.module.ModuleScope != nil {
				c.checkBinding(c.module.ModuleScope, node, false)
			}
		case *ast.ConstDecl:
			if c.module.ModuleScope != nil {
				c.checkBinding(c.module.ModuleScope, node, true)
			}
		}
		return true
	})
	ast.ForEachDecl(c.module.AST, func(decl ast.Decl) bool {
		switch node := decl.(type) {
		case *ast.FnDecl:
			if node == nil {
				return true
			}
			var sym *symbols.Symbol
			if node.Receiver != nil {
				sym = c.module.Semantics.MethodSymbol[node.ID()]
				c.checkReceiverFunction(node)
			} else {
				sym, _ = c.module.ModuleScope.Lookup(node.Name.Name)
			}
			if sym == nil {
				return true
			}
			c.checkFunction(sym, node)
		}
		return true
	})
}

func Check(ctx *project.CompilerContext, module *project.Module) {
	if module == nil || ctx == nil {
		return
	}
	(&checker{ctx: ctx, module: module}).checkModule()
}

// CanAdaptFirstCallArgument reports whether argType can occupy a function's
// first parameter through ordinary assignment or method/pipe adaptation.
// Addressability and mutability remain call-site checks, not discovery filters.
func CanAdaptFirstCallArgument(ctx *project.CompilerContext, module *project.Module, paramType, argType typeinfo.Type) bool {
	if ctx == nil || module == nil || paramType == nil || argType == nil {
		return false
	}
	checker := &checker{ctx: ctx, module: module}
	if checker.assignable(paramType, argType, nil) {
		return true
	}
	target, _, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(paramType))
	return reference && checker.matchesImplicitCallTarget(target, argType)
}
