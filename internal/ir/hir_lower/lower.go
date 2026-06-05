package hir_lower

import (
	"fmt"

	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/utils/numeric"
)

func GenerateHIR(ctx *context.CompilerContext, module *context.Module) *hir.Module {
	if module == nil {
		return nil
	}
	out := &hir.Module{
		Name:    module.ImportPath,
		Externs: make([]hir.Extern, 0),
		Funcs:   make([]*hir.Function, 0),
	}
	for _, decl := range module.AST.Decls {
		if fn, ok := decl.(*ast.FnDecl); ok && fn != nil {
			sym, found := module.ModuleScope.Lookup(fn.Name.Name)
			if !found || sym == nil {
				continue
			}
			if fn.Body == nil {
				fnType, _ := symbolType(sym)
				resolvedFnType, _ := fnType.(*typeinfo.FuncType)
				params := make([]ir.Param, 0, len(fn.Params))
				for i, param := range fn.Params {
					name := ""
					if param.Name != nil {
						name = param.Name.Name
					}
					paramType := typeinfo.TypeFromSyntax(param.Type)
					if resolvedFnType != nil && i < len(resolvedFnType.Params) && resolvedFnType.Params[i] != nil {
						paramType = resolvedFnType.Params[i]
					}
					params = append(params, ir.Param{Name: name, Type: typeinfo.TypeText(paramType)})
				}
				returnType := typeinfo.TypeFromSyntax(fn.ReturnType)
				if resolvedFnType != nil && resolvedFnType.Return != nil {
					returnType = resolvedFnType.Return
				}
				out.Externs = append(out.Externs, hir.Extern{
					Name:       sym.Name,
					Params:     params,
					ReturnType: typeinfo.TypeText(returnType),
				})
			} else {
				hirFn := lowerASTFunction(ctx, module, sym, fn)
				if hirFn != nil {
					out.Funcs = append(out.Funcs, hirFn)
				}
			}
		}
	}
	return out
}

func lowerASTFunction(ctx *context.CompilerContext, module *context.Module, sym *symbols.Symbol, fn *ast.FnDecl) *hir.Function {
	if sym == nil || fn == nil || fn.Body == nil || sym.Scope == nil {
		return nil
	}
	funcScope := sym.Scope.(*table.Scope)
	retType, ok := symbolType(sym)
	if ok {
		if fnType, ok := retType.(*typeinfo.FuncType); ok && fnType != nil {
			retType = fnType.Return
		}
	}
	if !ok || retType == nil {
		retType = typeinfo.TypeFromSyntax(fn.ReturnType)
	}
	retTypeStr := typeinfo.TypeText(retType)
	hirFn := &hir.Function{
		Name:       sym.Name,
		Params:     make([]ir.Param, 0, len(fn.Params)),
		ReturnType: retTypeStr,
		Body:       &hir.Block{Stmts: make([]hir.Stmt, 0), Location: fn.Body.Loc()},
		Location:   fn.Loc(),
	}
	for _, param := range fn.Params {
		name := ""
		paramType := typeinfo.TypeFromSyntax(param.Type)
		if param.Name != nil {
			sym, ok := funcScope.LookupLocal(param.Name.Name)
			if ok && sym != nil {
				name = symbolName(sym)
				if t, ok := symbolType(sym); ok {
					paramType = t
				}
			} else {
				name = param.Name.Name
			}
		}
		hirFn.Params = append(hirFn.Params, ir.Param{Name: name, Type: typeinfo.TypeText(paramType)})
	}
	appendBlock(module, funcScope, hirFn.Body, fn.Body, retTypeStr, ctx)
	return hirFn
}

func appendBlock(module *context.Module, parentScope *table.Scope, out *hir.Block, block *ast.BlockStmt, returnType string, ctx *context.CompilerContext) {
	if out == nil || block == nil {
		return
	}
	out.Location = block.Loc()
	scope := parentScope
	if module.Semantics != nil {
		if s, ok := module.Semantics.BlockScopes[block.ID()]; ok && s != nil {
			scope = s
		}
	}
	for _, stmt := range block.Stmts {
		appendStmt(module, scope, out, stmt, returnType, ctx)
	}
}

func appendStmt(module *context.Module, scope *table.Scope, out *hir.Block, stmt ast.Stmt, returnType string, ctx *context.CompilerContext) {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Loc()}
		appendBlock(module, scope, block, node, returnType, ctx)
		out.Stmts = append(out.Stmts, block)

	case *ast.LetDecl:
		if node.Name == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "let binding missing name", Location: node.Loc()})
			return
		}
		sym, ok := scope.LookupLocal(node.Name.Name)
		if !ok || sym == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "let binding missing symbol: " + node.Name.Name, Location: node.Loc()})
			return
		}
		symTypeStr := symTypeText(sym)
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			valueExpr = lowerASTExpr(ctx, module, scope, node.Value, symTypeStr)
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(sym), Constant: false, Value: valueExpr, Location: node.Loc()})

	case *ast.ConstDecl:
		if node.Name == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "const binding missing name", Location: node.Loc()})
			return
		}
		sym, ok := scope.LookupLocal(node.Name.Name)
		if !ok || sym == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "const binding missing symbol: " + node.Name.Name, Location: node.Loc()})
			return
		}
		symTypeStr := symTypeText(sym)
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			valueExpr = lowerASTExpr(ctx, module, scope, node.Value, symTypeStr)
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(sym), Constant: true, Value: valueExpr, Location: node.Loc()})

	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if node.Cond != nil {
			condExpr = lowerASTExpr(ctx, module, scope, node.Cond, "bool")
		}
		ifStmt := &hir.If{
			Cond:     condExpr,
			Then:     &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Then.Loc()},
			Location: node.Loc(),
		}
		appendBlock(module, scope, ifStmt.Then, node.Then, returnType, ctx)
		if node.Else != nil {
			ifStmt.Else = lowerElse(module, scope, node.Else, returnType, ctx)
		}
		out.Stmts = append(out.Stmts, ifStmt)

	case *ast.ReturnStmt:
		if node.Value == nil {
			out.Stmts = append(out.Stmts, &hir.Return{Value: &ir.InvalidExpr{Message: "missing return value", Type: "<invalid>"}, Location: node.Loc()})
			return
		}
		valueExpr := lowerASTExpr(ctx, module, scope, node.Value, returnType)
		out.Stmts = append(out.Stmts, &hir.Return{Value: valueExpr, Location: node.Loc()})

	case *ast.ExprStmt:
		if node.Expr == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "expression statement missing expression", Location: node.Loc()})
			return
		}
		valueExpr := lowerASTExpr(ctx, module, scope, node.Expr, "")
		out.Stmts = append(out.Stmts, &hir.ExprStmt{Value: valueExpr, Location: node.Loc()})
	}
}

func lowerElse(module *context.Module, scope *table.Scope, stmt ast.Stmt, returnType string, ctx *context.CompilerContext) hir.Stmt {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Loc()}
		appendBlock(module, scope, block, node, returnType, ctx)
		return block
	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if node.Cond != nil {
			condExpr = lowerASTExpr(ctx, module, scope, node.Cond, "bool")
		}
		out := &hir.If{
			Cond:     condExpr,
			Then:     &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Then.Loc()},
			Location: node.Loc(),
		}
		appendBlock(module, scope, out.Then, node.Then, returnType, ctx)
		if node.Else != nil {
			out.Else = lowerElse(module, scope, node.Else, returnType, ctx)
		}
		return out
	default:
		return &hir.Invalid{Message: "unsupported else branch", Location: node.Loc()}
	}
}

// lowerASTExpr directly lowers an AST expression to an IR expression using
// the module context's resolved expression types side-table.
func lowerASTExpr(ctx *context.CompilerContext, module *context.Module, scope *table.Scope, expr ast.Expr, expectedType string) ir.Expr {
	if expr == nil {
		return &ir.InvalidExpr{Message: "nil expression", Type: "<invalid>"}
	}

	// Fetch canonical type from the typechecker side-table when available.
	resolvedTypeStr := ""
	if module != nil && module.Semantics != nil {
		if t, ok := module.Semantics.ExprTypes[expr.ID()]; ok && t != nil {
			resolvedTypeStr = typeinfo.TypeText(t)
		}
	}

	switch node := expr.(type) {
	case *ast.NumberLit:
		t := resolvedTypeStr
		if t == "" {
			t = expectedType
		}
		return lowerNumberLit(node, t)

	case *ast.StringLit:
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" {
			t = "cstr"
		}
		return &ir.StringLit{Value: node.Value, Type: t}

	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok || sym == nil {
			return &ir.InvalidExpr{Message: "unresolved identifier: " + node.Name, Type: "<invalid>"}
		}
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" || t == "<unknown>" {
			t = symTypeText(sym)
		}
		return &ir.Ident{Name: symbolName(sym), Type: t}

	case *ast.ScopeResolution:
		if sym := lookupScopeResolutionSymbol(ctx, module, scope, node); sym != nil {
			t := resolvedTypeStr
			if t == "" || t == "<invalid>" || t == "<unknown>" {
				t = symTypeText(sym)
			}
			return &ir.Ident{Name: symbolName(sym), Type: t}
		}
		return &ir.InvalidExpr{Message: "unresolved qualified identifier: " + node.Module.Name + "::" + node.Name.Name, Type: "<invalid>"}

	case *ast.UnaryExpr:
		arg := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" {
			t = arg.TypeText()
			if node.Op == "!" {
				t = "bool"
			}
		}
		return &ir.Unary{Op: node.Op, Arg: arg, Type: t}

	case *ast.BinaryExpr:
		left := lowerASTExpr(ctx, module, scope, node.Left, expectedType)
		right := lowerASTExpr(ctx, module, scope, node.Right, expectedType)
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" {
			t = left.TypeText()
			switch node.Op {
			case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
				t = "bool"
			}
		}
		return &ir.Binary{Op: node.Op, Left: left, Right: right, Type: t}

	case *ast.CallExpr:
		calleeExpr := lowerASTExpr(ctx, module, scope, node.Callee, "")
		args := make([]ir.Expr, 0, len(node.Args))
		for _, arg := range node.Args {
			args = append(args, lowerASTExpr(ctx, module, scope, arg, ""))
		}
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" {
			var sym *symbols.Symbol
			switch callee := node.Callee.(type) {
			case *ast.Ident:
				if s, ok := scope.Lookup(callee.Name); ok {
					sym = s
				}
			case *ast.ScopeResolution:
				if s := lookupScopeResolutionSymbol(ctx, module, scope, callee); s != nil {
					sym = s
				}
			}
			if sym != nil {
				if fnType, ok := sym.Type.(*typeinfo.FuncType); ok && fnType != nil && fnType.Return != nil {
					t = typeinfo.TypeText(fnType.Return)
				}
			}
		}
		return &ir.Call{Callee: calleeExpr, Args: args, Type: t}

	case *ast.AsExpr:
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" {
			t = typeinfo.TypeText(typeinfo.TypeFromSyntax(node.TypeExpr))
		}
		subExpr := lowerASTExpr(ctx, module, scope, node.Expr, t)
		return &ir.Cast{Expr: subExpr, Type: t}

	default:
		return &ir.InvalidExpr{Message: "unsupported expression", Type: "<invalid>"}
	}
}

// lookupScopeResolutionSymbol resolves a ScopeResolution node in two steps:
// 1. Find the imported module by alias in module.Imports.
// 2. Look up the symbol in that module's scope.
// Returns nil if resolution fails.
func lookupScopeResolutionSymbol(ctx *context.CompilerContext, module *context.Module, _ *table.Scope, sr *ast.ScopeResolution) *symbols.Symbol {
	if ctx == nil || module == nil || sr == nil {
		return nil
	}
	alias := sr.Module.Name
	member := sr.Name.Name
	imp, ok := module.Imports[alias]
	if !ok {
		return nil
	}
	mod, ok := ctx.ModuleByKey(imp.Key)
	if !ok || mod == nil || mod.ModuleScope == nil {
		return nil
	}
	sym, ok := mod.ModuleScope.LookupLocal(member)
	if !ok || sym == nil {
		return nil
	}
	return sym
}

// lowerNumberLit produces the correct IR literal from a raw number token and
// the expected type string (e.g. "i8", "f32") set by the typechecker via symbol.Type.
func lowerNumberLit(node *ast.NumberLit, expectedType string) ir.Expr {
	if node == nil {
		return &ir.InvalidExpr{Message: "nil number literal", Type: "<invalid>"}
	}
	if expectedType == "" || expectedType == "<invalid>" || expectedType == "<unknown>" {
		// No expected type — use language default.
		if numeric.IsFloat(node.Value) {
			return &ir.FloatLit{Value: node.Value, Type: typeinfo.TypeText(typeinfo.DefaultNumberType(node.Value))}
		}
		return &ir.IntLit{Value: node.Value, Type: typeinfo.TypeText(typeinfo.DefaultNumberType(node.Value))}
	}
	if ir.IsFloatType(expectedType) {
		v := node.Value
		if !numeric.IsFloat(v) {
			// Convert integer text to float text for LLVM IR.
			if iv, err := numeric.StringToBigInt(v); err == nil {
				v = iv.String() + ".0"
			}
		}
		return &ir.FloatLit{Value: v, Type: expectedType}
	}
	return &ir.IntLit{Value: node.Value, Type: expectedType}
}

func symbolName(sym *symbols.Symbol) string {
	if sym == nil {
		return ""
	}
	return fmt.Sprintf("%s$%d", sym.Name, sym.ID)
}

func symbolType(sym *symbols.Symbol) (typeinfo.Type, bool) {
	if sym == nil || sym.Type == nil {
		return nil, false
	}
	typ, ok := sym.Type.(typeinfo.Type)
	return typ, ok && typ != nil
}

func symTypeText(sym *symbols.Symbol) string {
	if t, ok := symbolType(sym); ok {
		return typeinfo.TypeText(t)
	}
	return "<unknown>"
}
