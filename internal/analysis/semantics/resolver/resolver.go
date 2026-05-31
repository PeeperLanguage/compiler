package resolver

import (
	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"slices"
	"strings"
)

type resolver struct {
	ctx    *context.CompilerContext
	module *context.Module
}

func (r *resolver) resolveModule() {
	if r == nil || r.module == nil {
		return
	}
	r.module.ResetResolutions()
	for _, collectedFn := range r.module.Functions {
		if collectedFn == nil || collectedFn.Decl == nil || collectedFn.Scope == nil {
			continue
		}
		r.resolveFunction(collectedFn)
	}
}

func (r *resolver) resolveFunction(fn *declinfo.Function) {
	if r == nil || r.module == nil || fn == nil || fn.Decl == nil || fn.Scope == nil {
		return
	}
	if fn.Decl.Name != nil && fn.Symbol != nil {
		r.module.BindResolution(fn.Decl.Name, &declinfo.Resolution{
			Kind:   declinfo.ResolutionSymbol,
			Symbol: fn.Symbol,
		})
	}
	for _, param := range fn.Decl.Params {
		if param.Name == nil || param.Name.Name == "" {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, fn.Decl, diagnostics.ErrMissingIdentifier, "parameter name required")
			return
		}
		sym := symbols.New(param.Name.Name, symbols.SymbolParam, param.Name)
		if err := fn.Scope.Declare(sym); err != nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, param.Name, diagnostics.ErrRedeclaredSymbol, err.Error())
			return
		}
		r.module.BindResolution(param.Name, &declinfo.Resolution{
			Kind:   declinfo.ResolutionSymbol,
			Symbol: sym,
		})
	}
	r.resolveBlock(fn, fn.Scope, fn.Decl.Body)
}

func (r *resolver) resolveBlock(fn *declinfo.Function, scope *table.Scope, block *ast.BlockStmt) {
	if block == nil {
		return
	}
	for _, stmt := range block.Stmts {
		r.resolveStmt(fn, scope, stmt)
	}
}

func (r *resolver) resolveStmt(fn *declinfo.Function, scope *table.Scope, stmt ast.Stmt) {
	if stmt == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		r.resolveBlock(fn, table.New(scope), node)
	case *ast.LetDecl:
		sym := symbols.New(node.Name.Name, symbols.SymbolVar, node)
		sym.Initializing = true
		defer func() {
			sym.Initializing = false
		}()
		if err := scope.Declare(sym); err != nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrRedeclaredSymbol, err.Error())
			return
		}
		if node.Name != nil {
			r.module.BindResolution(node.Name, &declinfo.Resolution{
				Kind:   declinfo.ResolutionSymbol,
				Symbol: sym,
			})
		}
		if node.Value != nil {
			r.resolveExpr(fn, scope, node.Value)
		}
	case *ast.ConstDecl:
		sym := symbols.New(node.Name.Name, symbols.SymbolConst, node)
		sym.Initializing = true
		defer func() {
			sym.Initializing = false
		}()
		if err := scope.Declare(sym); err != nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrRedeclaredSymbol, err.Error())
			return
		}
		if node.Name != nil {
			r.module.BindResolution(node.Name, &declinfo.Resolution{
				Kind:   declinfo.ResolutionSymbol,
				Symbol: sym,
			})
		}
		if node.Value != nil {
			r.resolveExpr(fn, scope, node.Value)
		}
	case *ast.ReturnStmt:
		if node == nil {
			return
		}
		if node.Value == nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return
		}
		r.resolveExpr(fn, scope, node.Value)
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
			return
		}
		r.resolveExpr(fn, scope, node.Cond)
		r.resolveBlock(fn, table.New(scope), node.Then)
		if elseBlock, ok := node.Else.(*ast.BlockStmt); ok {
			r.resolveBlock(fn, table.New(scope), elseBlock)
		}
		r.resolveStmt(fn, scope, node.Else)
	case *ast.ExprStmt:
		r.resolveExpr(fn, scope, node.Expr)
	default:
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidStatement, "unsupported statement for arithmetic flow")
		return
	}
}

func (r *resolver) resolveExpr(fn *declinfo.Function, scope *table.Scope, expr ast.Expr) {
	switch node := expr.(type) {
	case *ast.NumberLit:
		// nothing to look for literals
		return
	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if ok && sym != nil {
			if sym.Kind == symbols.SymbolImport {
				common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidExpression,
					"import alias must be qualified with `::`")
				return
			}
			if sym.Initializing {
				msg := "symbol `" + node.Name + "` used before it's defined"
				r.ctx.Diagnostics.Add(
					diagnostics.NewError(msg).
						WithCode(diagnostics.ErrUseBeforeDecl).
						WithPrimaryLabel(node.Loc(), msg).
						WithHelp("rename binding or use earlier value"),
				)
				return
			}
			r.module.BindResolution(node, &declinfo.Resolution{
				Kind:   declinfo.ResolutionSymbol,
				Symbol: sym,
			})
			return
		}
		if strings.Contains(node.Name, "::") {
			if r.resolveQualifiedIdent(node) {
				return
			}
		}
		reportUnresolved(r.module, fn, scope, node, r.ctx.Diagnostics)
	case *ast.UnaryExpr:
		r.resolveExpr(fn, scope, node.Expr)
	case *ast.BinaryExpr:
		r.resolveExpr(fn, scope, node.Left)
		r.resolveExpr(fn, scope, node.Right)
	case *ast.CallExpr:
		r.resolveExpr(fn, scope, node.Callee)
		for _, arg := range node.Args {
			r.resolveExpr(fn, scope, arg)
		}
	case *ast.AsExpr:
		// Resolve the expression being cast
		r.resolveExpr(fn, scope, node.Expr)
		// The type expression doesn't need resolution
	default:
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrInvalidExpression, "unsupported expression for arithmetic flow")
	}
}

func Resolve(ctx *context.CompilerContext, module *context.Module) {
	if module == nil || ctx == nil {
		return
	}
	r := &resolver{module: module, ctx: ctx}
	r.resolveModule()
}

func (r *resolver) resolveQualifiedIdent(node *ast.Ident) bool {
	if r == nil || r.module == nil || node == nil {
		return false
	}
	qualifier, member, ok := splitQualifiedName(node.Name)
	if !ok {
		return false
	}
	imp, ok := r.module.Imports[qualifier]
	if !ok {
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrModuleNotFound,
			"unknown import alias `"+qualifier+"`")
		return false
	}
	mod, ok := r.ctx.ModuleByKey(imp.Key)
	if !ok || mod == nil || mod.ModuleScope == nil {
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrModuleNotFound,
			"imported module not loaded for `"+qualifier+"`")
		return false
	}
	sym, ok := mod.ModuleScope.LookupLocal(member)
	if !ok || sym == nil {
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrUndefinedSymbol,
			"unknown identifier `"+member+"` in module `"+qualifier+"`")
		return false
	}
	if !sym.IsPub {
		common.AddError(r.ctx.Diagnostics, r.module.FilePath, node, diagnostics.ErrSymbolNotExported,
			"`"+member+"` is not exported from `"+qualifier+"`")
		return false
	}
	r.module.BindResolution(node, &declinfo.Resolution{
		Kind:   declinfo.ResolutionSymbol,
		Symbol: sym,
	})
	return true
}

func splitQualifiedName(name string) (string, string, bool) {
	if name == "" {
		return "", "", false
	}
	parts := strings.Split(name, "::")
	if len(parts) < 2 {
		return "", "", false
	}
	if parts[0] == "" {
		return "", "", false
	}
	if slices.Contains(parts[1:], "") {
		return "", "", false
	}
	return parts[0], strings.Join(parts[1:], "::"), true
}
