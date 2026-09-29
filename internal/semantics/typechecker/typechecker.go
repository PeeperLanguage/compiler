package typechecker

import (
	"compiler/internal/constvalue"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

type checker struct {
	ctx          *project.CompilerContext
	module       *module.Module
	evidence     *evidence
	constantEval *constantEvaluator

	payloadContext      int
	optionalTestContext int
	wholeCarrierExpr    ast.Expr
	loopDepth           int
	// reusedCall is the already-checked source call embedded at the root of a
	// generated producer loop. Nested calls and flow visits still check normally.
	reusedCall *ast.CallExpr
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

func (c *checker) exprType(id source.NodeID) typeinfo.Type {
	if c == nil || c.evidence == nil {
		return nil
	}
	return c.evidence.ExprType(id)
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
			sym := c.module.SymbolIndex.Symbol(node.Name)
			if node.Receiver != nil {
				c.checkReceiverFunction(node)
			}
			if sym == nil {
				return true
			}
			c.checkFunction(sym, node)
		}
		return true
	})
}

func Check(ctx *project.CompilerContext, module *module.Module) *thir.Module {
	source, _ := runCheck(ctx, module)
	return source
}

func runCheck(ctx *project.CompilerContext, module *module.Module) (*thir.Module, *evidence) {
	if module == nil || ctx == nil {
		return nil, nil
	}
	evidence := newEvidence()
	c := &checker{ctx: ctx, module: module, evidence: evidence}
	c.constantEval = newConstantEvaluator(ctx, module, evidence)
	c.checkModule()
	c.constantEval.finalizeModuleValues()
	return c.buildTHIR(), evidence
}

func (c *checker) evaluateConstant(ctx *project.CompilerContext, scope *symbols.Scope, expr ast.Expr, expected typeinfo.Type) (constvalue.Value, bool) {
	if c == nil || c.module == nil || expr == nil {
		return nil, false
	}
	if c.constantEval == nil {
		c.constantEval = newConstantEvaluator(c.ctx, c.module, c.evidence)
	}
	evaluator := c.constantEval.withContext(ctx)
	return evaluator.evalExpr(scope, expr, expected)
}

// CanAdaptFirstCallArgument reports whether argType can occupy a function's
// first parameter through ordinary assignment or method/pipe adaptation.
// Addressability and mutability remain call-site checks, not discovery filters.
func CanAdaptFirstCallArgument(ctx *project.CompilerContext, module *module.Module, paramType, argType typeinfo.Type) bool {
	if ctx == nil || module == nil || paramType == nil || argType == nil {
		return false
	}
	checker := &checker{ctx: ctx, module: module, evidence: newEvidence()}
	if checker.isAssignable(paramType, argType, nil) {
		return true
	}
	target, _, isReference := typeinfo.ReferenceTarget(typeinfo.Underlying(paramType))
	return isReference && checker.matchesImplicitCallTarget(target, argType)
}
