package hir_lower

import (
	"fmt"

	"compiler/internal/analysis/semantics/declinfo"
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
		Externs: make([]hir.Extern, 0, len(module.Externs)),
		Funcs:   make([]*hir.Function, 0, len(module.Functions)),
	}
	for _, ex := range module.Externs {
		if ex.Symbol == nil || ex.Decl == nil {
			continue
		}
		fnType, _ := symbolType(ex.Symbol)
		resolvedFnType, _ := fnType.(*typeinfo.FuncType)
		params := make([]ir.Param, 0, len(ex.Decl.Params))
		for i, param := range ex.Decl.Params {
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
		returnType := typeinfo.TypeFromSyntax(ex.Decl.ReturnType)
		if resolvedFnType != nil && resolvedFnType.Return != nil {
			returnType = resolvedFnType.Return
		}
		out.Externs = append(out.Externs, hir.Extern{
			Name:       ex.Symbol.Name,
			Params:     params,
			ReturnType: typeinfo.TypeText(returnType),
		})
	}
	for _, declFn := range module.Functions {
		if declFn == nil || declFn.Decl == nil || declFn.Scope == nil {
			continue
		}
		fnSym := declFn.Symbol
		if fnSym == nil {
			continue
		}
		retType, ok := symbolType(fnSym)
		if ok {
			if fnType, ok := retType.(*typeinfo.FuncType); ok && fnType != nil {
				retType = fnType.Return
			}
		}
		if !ok || retType == nil {
			retType = typeinfo.TypeFromSyntax(declFn.Decl.ReturnType)
		}
		retTypeStr := typeinfo.TypeText(retType)
		fn := &hir.Function{
			Name:       fnSym.Name,
			Params:     make([]ir.Param, 0, len(declFn.Decl.Params)),
			ReturnType: retTypeStr,
			Body:       &hir.Block{Stmts: make([]hir.Stmt, 0), Location: declFn.Decl.Body.Loc()},
			Location:   declFn.Decl.Loc(),
		}
		for _, param := range declFn.Decl.Params {
			name := ""
			paramType := typeinfo.TypeFromSyntax(param.Type)
			if param.Name != nil {
				// Look up the parameter symbol in the function scope to get the resolved type.
				sym, ok := declFn.Scope.LookupLocal(param.Name.Name)
				if ok && sym != nil {
					name = symbolName(sym)
					if t, ok := symbolType(sym); ok {
						paramType = t
					}
				} else {
					name = param.Name.Name
				}
			}
			fn.Params = append(fn.Params, ir.Param{Name: name, Type: typeinfo.TypeText(paramType)})
		}
		appendBlock(declFn, fn.Body, declFn.Decl.Body, retTypeStr, ctx, module)
		out.Funcs = append(out.Funcs, fn)
	}
	return out
}

func appendBlock(fn *declinfo.Function, out *hir.Block, block *ast.BlockStmt, returnType string, ctx *context.CompilerContext, module *context.Module) {
	if out == nil || block == nil {
		return
	}
	out.Location = block.Loc()
	// Use the scope that the resolver stored for this block.
	scope := fn.Scope
	if fn.BlockScopes != nil {
		if s, ok := fn.BlockScopes[block]; ok {
			scope = s
		}
	}
	for _, stmt := range block.Stmts {
		appendStmt(fn, scope, out, stmt, returnType, ctx, module)
	}
}

// scopeForBlock is a dead helper left from before — removed.

func appendStmt(fn *declinfo.Function, scope *table.Scope, out *hir.Block, stmt ast.Stmt, returnType string, ctx *context.CompilerContext, module *context.Module) {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		// Nested block — look up its stored scope from the resolver.
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Loc()}
		appendBlock(fn, block, node, returnType, ctx, module)
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
		appendBlock(fn, ifStmt.Then, node.Then, returnType, ctx, module)
		if node.Else != nil {
			ifStmt.Else = lowerElse(fn, node.Else, returnType, ctx, module)
		}
		out.Stmts = append(out.Stmts, ifStmt)

	case *ast.ReturnStmt:
		if node.Value == nil {
			out.Stmts = append(out.Stmts, &hir.Return{Value: &ir.InvalidExpr{Message: "missing return value", Type: "<invalid>"}, Location: node.Loc()})
			return
		}
		valueExpr := lowerASTExpr(ctx, module, scope, node.Value, returnType)
		out.Stmts = append(out.Stmts, &hir.Return{Value: valueExpr, Location: node.Loc()})
	}
}

func lowerElse(fn *declinfo.Function, stmt ast.Stmt, returnType string, ctx *context.CompilerContext, module *context.Module) hir.Stmt {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Loc()}
		appendBlock(fn, block, node, returnType, ctx, module)
		return block
	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if node.Cond != nil {
			enclosingScope := fn.Scope
			condExpr = lowerASTExpr(ctx, module, enclosingScope, node.Cond, "bool")
		}
		out := &hir.If{
			Cond:     condExpr,
			Then:     &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Then.Loc()},
			Location: node.Loc(),
		}
		appendBlock(fn, out.Then, node.Then, returnType, ctx, module)
		if node.Else != nil {
			out.Else = lowerElse(fn, node.Else, returnType, ctx, module)
		}
		return out
	default:
		return &hir.Invalid{Message: "unsupported else branch", Location: node.Loc()}
	}
}

// lowerASTExpr directly lowers an AST expression to an IR expression using
// scope lookup for symbol resolution and expectedType for literal coercion.
func lowerASTExpr(ctx *context.CompilerContext, module *context.Module, scope *table.Scope, expr ast.Expr, expectedType string) ir.Expr {
	if expr == nil {
		return &ir.InvalidExpr{Message: "nil expression", Type: "<invalid>"}
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		return lowerNumberLit(node, expectedType)

	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok || sym == nil {
			return &ir.InvalidExpr{Message: "unresolved identifier: " + node.Name, Type: "<invalid>"}
		}
		return &ir.Ident{Name: symbolName(sym), Type: symTypeText(sym)}

	case *ast.ScopeResolution:
		if sym := lookupScopeResolutionSymbol(ctx, module, scope, node); sym != nil {
			return &ir.Ident{Name: symbolName(sym), Type: symTypeText(sym)}
		}
		return &ir.InvalidExpr{Message: "unresolved qualified identifier: " + node.Module.Name + "::" + node.Name.Name, Type: "<invalid>"}

	case *ast.UnaryExpr:
		arg := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		exprType := arg.TypeText()
		if node.Op == "!" {
			exprType = "bool"
		}
		return &ir.Unary{Op: node.Op, Arg: arg, Type: exprType}

	case *ast.BinaryExpr:
		left := lowerASTExpr(ctx, module, scope, node.Left, expectedType)
		right := lowerASTExpr(ctx, module, scope, node.Right, expectedType)
		exprType := left.TypeText()
		switch node.Op {
		case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
			exprType = "bool"
		}
		return &ir.Binary{Op: node.Op, Left: left, Right: right, Type: exprType}

	case *ast.CallExpr:
		calleeExpr := lowerASTExpr(ctx, module, scope, node.Callee, "")
		args := make([]ir.Expr, 0, len(node.Args))
		for _, arg := range node.Args {
			args = append(args, lowerASTExpr(ctx, module, scope, arg, ""))
		}
		// Get the return type from the callee symbol (handles qualified callees too).
		retType := "<invalid>"
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
				retType = typeinfo.TypeText(fnType.Return)
			}
		}
		return &ir.Call{Callee: calleeExpr, Args: args, Type: retType}

	case *ast.AsExpr:
		targetTypeStr := typeinfo.TypeText(typeinfo.TypeFromSyntax(node.TypeExpr))
		subExpr := lowerASTExpr(ctx, module, scope, node.Expr, targetTypeStr)
		return &ir.Cast{Expr: subExpr, Type: targetTypeStr}

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
