package hir_lower

import (
	"fmt"
	"slices"
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"compiler/pkg/numeric"
)

var currentModuleScope *table.Scope

func GenerateHIR(ctx *project.CompilerContext, module *project.Module) *hir.Module {
	if module == nil {
		return nil
	}
	currentModuleScope = module.ModuleScope
	defer func() {
		currentModuleScope = nil
	}()
	out := &hir.Module{
		Name:     module.ImportPath,
		FilePath: module.FilePath,
		Externs:  make([]hir.Extern, 0),
		Funcs:    make([]*hir.Function, 0),
	}
	for _, decl := range module.AST.Decls {
		switch node := decl.(type) {
		case *ast.FnDecl:
			fn := node
			sym, found := module.ModuleScope.Lookup(fn.Name.Name)
			if !found || sym == nil {
				continue
			}
			if fn.Body == nil {
				fnType, _ := symbols.GetSymbolType(sym)
				resolvedFnType, _ := fnType.(*typeinfo.FuncType)
				params, returnType := lowerExternSignature(fn.Params, fn.ReturnType, resolvedFnType)
				out.Externs = append(out.Externs, hir.Extern{
					Name:       sym.Name,
					Params:     params,
					ReturnType: returnType,
				})
			} else {
				hirFn := lowerASTFunctionNamed(ctx, module, sym, fn, sym.Name)
				if hirFn != nil {
					out.Funcs = append(out.Funcs, hirFn)
				}
			}
		case *ast.ImplDecl:
			lowerImplDecl(ctx, module, out, node)
		}
	}
	return out
}

func lowerImplDecl(ctx *project.CompilerContext, module *project.Module, out *hir.Module, decl *ast.ImplDecl) {
	if module == nil || out == nil || decl == nil || decl.Target == nil || module.Semantics == nil {
		return
	}
	targetType := typeinfo.TypeFromSyntax(decl.Target)
	targetText := typeinfo.TypeText(targetType)
	for _, method := range decl.Methods {
		if method == nil || method.Name == nil {
			continue
		}
		sym, ok := module.Semantics.MethodSymbol[method.ID()]
		if !ok || sym == nil {
			continue
		}
		if method.Body == nil {
			fnType, _ := symbols.GetSymbolType(sym)
			resolvedFnType, _ := fnType.(*typeinfo.FuncType)
			params, returnType := lowerExternSignature(method.Params, method.ReturnType, resolvedFnType)
			out.Externs = append(out.Externs, hir.Extern{
				Name:       methodFunctionName(targetText, method.Name.Name),
				Params:     params,
				ReturnType: returnType,
			})
			continue
		}
		hirFn := lowerASTFunctionNamed(ctx, module, sym, method, methodFunctionName(targetText, method.Name.Name))
		if hirFn != nil {
			out.Funcs = append(out.Funcs, hirFn)
		}
	}
}

func lowerExternSignature(params []ast.Param, fallbackReturnType ast.TypeExpr, resolvedFnType *typeinfo.FuncType) ([]ir.Param, string) {
	loweredParams := make([]ir.Param, 0, len(params))
	for i, param := range params {
		name := ""
		if param.Name != nil {
			name = param.Name.Name
		}
		paramType := typeinfo.TypeFromSyntax(param.Type)
		if resolvedFnType != nil && i < len(resolvedFnType.Params) && resolvedFnType.Params[i] != nil {
			paramType = resolvedFnType.Params[i]
		}
		loweredParams = append(loweredParams, ir.Param{Name: name, Type: loweredTypeText(paramType)})
	}

	returnType := typeinfo.TypeFromSyntax(fallbackReturnType)
	if resolvedFnType != nil && resolvedFnType.Return != nil {
		returnType = resolvedFnType.Return
	}
	return loweredParams, loweredReturnTypeText(returnType)
}

func lowerASTFunctionNamed(ctx *project.CompilerContext, module *project.Module, sym *symbols.Symbol, fn *ast.FnDecl, emittedName string) *hir.Function {
	if sym == nil || fn == nil || fn.Body == nil || sym.Scope == nil {
		return nil
	}
	funcScope := sym.Scope.(*table.Scope)
	retType, ok := symbols.GetSymbolType(sym)
	if ok {
		if fnType, ok := retType.(*typeinfo.FuncType); ok && fnType != nil {
			retType = fnType.Return
		}
	}
	if !ok || retType == nil {
		retType = typeinfo.TypeFromSyntax(fn.ReturnType)
	}
	retTypeStr := loweredReturnTypeText(retType)
	hirFn := &hir.Function{
		Name:       emittedName,
		Params:     make([]ir.Param, 0, len(fn.Params)),
		ReturnType: retTypeStr,
		Body:       &hir.Block{Stmts: make([]hir.Stmt, 0), Location: ast.LocOf(fn.Body)},
		Location:   ast.LocOf(fn),
	}
	for _, param := range fn.Params {
		name := ""
		paramType := typeinfo.TypeFromSyntax(param.Type)
		if param.Name != nil {
			sym, ok := funcScope.LookupLocal(param.Name.Name)
			if ok && sym != nil {
				name = symbolName(sym)
				if t, ok := symbols.GetSymbolType(sym); ok {
					paramType = t
				}
			} else {
				name = param.Name.Name
			}
		}
		hirFn.Params = append(hirFn.Params, ir.Param{Name: name, Type: loweredTypeText(paramType)})
	}
	appendBlock(module, funcScope, hirFn.Body, fn.Body, retType, ctx)
	return hirFn
}

func appendBlock(module *project.Module, parentScope *table.Scope, out *hir.Block, block *ast.BlockStmt, returnType typeinfo.Type, ctx *project.CompilerContext) {
	if out == nil || block == nil {
		return
	}
	out.Location = ast.LocOf(block)
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

func appendStmt(module *project.Module, scope *table.Scope, out *hir.Block, stmt ast.Stmt, returnType typeinfo.Type, ctx *project.CompilerContext) {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), Location: ast.LocOf(node)}
		appendBlock(module, scope, block, node, returnType, ctx)
		out.Stmts = append(out.Stmts, block)

	case *ast.LetDecl:
		if node.Name == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "let binding missing name", Location: ast.LocOf(node)})
			return
		}
		sym, ok := scope.LookupLocal(node.Name.Name)
		if !ok || sym == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "let binding missing symbol: " + node.Name.Name, Location: ast.LocOf(node)})
			return
		}
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			valueExpr = lowerASTExpr(ctx, module, scope, node.Value, sym.Type)
		}
		if shouldDiscardBindingValue(module, sym.ID) {
			out.Stmts = append(out.Stmts, &hir.ExprStmt{Value: valueExpr, Location: ast.LocOf(node)})
			return
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(sym), Constant: false, Value: valueExpr, Location: ast.LocOf(node)})

	case *ast.ConstDecl:
		if node.Name == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "const binding missing name", Location: ast.LocOf(node)})
			return
		}
		sym, ok := scope.LookupLocal(node.Name.Name)
		if !ok || sym == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "const binding missing symbol: " + node.Name.Name, Location: ast.LocOf(node)})
			return
		}
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			valueExpr = lowerASTExpr(ctx, module, scope, node.Value, sym.Type)
		}
		if shouldDiscardBindingValue(module, sym.ID) {
			out.Stmts = append(out.Stmts, &hir.ExprStmt{Value: valueExpr, Location: ast.LocOf(node)})
			return
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(sym), Constant: true, Value: valueExpr, Location: ast.LocOf(node)})

	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if node.Cond != nil {
			condExpr = lowerASTExpr(ctx, module, scope, node.Cond, &typeinfo.BoolType{})
		}
		ifStmt := &hir.If{
			Cond:     condExpr,
			Then:     &hir.Block{Stmts: make([]hir.Stmt, 0), Location: ast.LocOf(node.Then)},
			Location: ast.LocOf(node),
		}
		appendBlock(module, scope, ifStmt.Then, node.Then, returnType, ctx)
		if node.Else != nil {
			ifStmt.Else = lowerElse(module, scope, node.Else, returnType, ctx)
		}
		out.Stmts = append(out.Stmts, ifStmt)

	case *ast.ReturnStmt:
		if node.Value == nil {
			out.Stmts = append(out.Stmts, &hir.Return{Location: ast.LocOf(node)})
			return
		}
		valueExpr := lowerASTExpr(ctx, module, scope, node.Value, returnType)
		out.Stmts = append(out.Stmts, &hir.Return{Value: valueExpr, Location: ast.LocOf(node)})

	case *ast.ExprStmt:
		if node.Expr == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "expression statement missing expression", Location: ast.LocOf(node)})
			return
		}
		valueExpr := lowerASTExpr(ctx, module, scope, node.Expr, nil)
		out.Stmts = append(out.Stmts, &hir.ExprStmt{Value: valueExpr, Location: ast.LocOf(node)})
	case *ast.AssignStmt:
		if node.Target == nil || node.Value == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "assignment missing target or value", Location: ast.LocOf(node)})
			return
		}
		targetExpr := lowerAssignTargetExpr(ctx, module, scope, node.Target)
		targetType := exprResolvedType(module, node.Target)
		valueExpr := lowerASTExpr(ctx, module, scope, node.Value, targetType)
		out.Stmts = append(out.Stmts, &hir.Assign{Target: targetExpr, Value: valueExpr, Location: ast.LocOf(node)})
	}
}

func lowerAssignTargetExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, expr ast.Expr) ir.Expr {
	if selector, ok := expr.(*ast.SelectorExpr); ok && selector != nil {
		baseType := exprResolvedType(module, selector.Expr)
		if fieldType, fieldIndex, ok := lookupStructField(baseType, selector.Name.Name); ok {
			if _, throughPtr := baseType.(*typeinfo.RawPtrType); throughPtr {
				return &ir.Field{
					Base:       lowerASTExpr(ctx, module, scope, selector.Expr, nil),
					Index:      fieldIndex,
					ThroughPtr: true,
					Type:       loweredTypeText(fieldType),
					Location:   ast.LocOf(selector),
				}
			}
			if isAddressableExpr(module, scope, selector.Expr) {
				return &ir.Field{
					Base: &ir.AddrOf{
						Expr:     lowerAssignTargetExpr(ctx, module, scope, selector.Expr),
						Type:     "^" + loweredTypeText(baseType),
						Location: ast.LocOf(selector.Expr),
					},
					Index:      fieldIndex,
					ThroughPtr: true,
					Type:       loweredTypeText(fieldType),
					Location:   ast.LocOf(selector),
				}
			}
		}
	}
	return lowerASTExpr(ctx, module, scope, expr, nil)
}

func isAddressableExpr(module *project.Module, scope *table.Scope, expr ast.Expr) bool {
	if scope == nil || expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return scope.IsMutableVar(e.Name)
	case *ast.SelectorExpr:
		baseType := exprResolvedType(module, e.Expr)
		if _, ok := baseType.(*typeinfo.RawPtrType); ok {
			return true
		}
		return isAddressableExpr(module, scope, e.Expr)
	}
	return false
}

func lowerElse(module *project.Module, scope *table.Scope, stmt ast.Stmt, returnType typeinfo.Type, ctx *project.CompilerContext) hir.Stmt {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), Location: ast.LocOf(node)}
		appendBlock(module, scope, block, node, returnType, ctx)
		return block
	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if node.Cond != nil {
			condExpr = lowerASTExpr(ctx, module, scope, node.Cond, &typeinfo.BoolType{})
		}
		out := &hir.If{
			Cond:     condExpr,
			Then:     &hir.Block{Stmts: make([]hir.Stmt, 0), Location: ast.LocOf(node.Then)},
			Location: ast.LocOf(node),
		}
		appendBlock(module, scope, out.Then, node.Then, returnType, ctx)
		if node.Else != nil {
			out.Else = lowerElse(module, scope, node.Else, returnType, ctx)
		}
		return out
	default:
		return &hir.Invalid{Message: "unsupported else branch", Location: ast.LocOf(node)}
	}
}

// lowerASTExpr directly lowers an AST expression to an IR expression using
// the module context's resolved expression types side-table.
func lowerASTExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, expr ast.Expr, expectedType typeinfo.Type) ir.Expr {
	if expr == nil {
		return &ir.InvalidExpr{Message: "nil expression", Type: "<invalid>"}
	}
	loc := ast.LocOf(expr)

	// Fetch canonical type from the typechecker side-table when available.
	resolvedTypeStr := ""
	if t := exprResolvedType(module, expr); t != nil {
		resolvedTypeStr = loweredTypeText(t)
	}
	if ifaceExpr := maybeLowerInterfaceExpr(ctx, module, scope, expr, expectedType); ifaceExpr != nil {
		return ifaceExpr
	}
	expectedTypeStr := loweredTypeText(expectedType)

	switch node := expr.(type) {
	case *ast.NumberLit:
		t := resolvedTypeStr
		if t == "" {
			t = expectedTypeStr
		}
		return lowerNumberLit(node, t, loc)

	case *ast.StringLit:
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" {
			t = "cstr"
		}
		return &ir.StringLit{Value: node.Value, Type: t, Location: loc}

	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok || sym == nil {
			return &ir.InvalidExpr{Message: "unresolved identifier: " + node.Name, Type: "<invalid>", Location: loc}
		}
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" || t == "<unknown>" {
			if symType, ok := symbols.GetSymbolType(sym); ok {
				t = loweredTypeText(symType)
			} else {
				t = "<unknown>"
			}
		}
		return &ir.Ident{Name: symbolName(sym), Type: t, Location: loc}

	case *ast.ScopeResolution:
		if sym := lookupScopeResolutionSymbol(ctx, module, scope, node); sym != nil {
			t := resolvedTypeStr
			if t == "" || t == "<invalid>" || t == "<unknown>" {
				if symType, ok := symbols.GetSymbolType(sym); ok {
					t = loweredTypeText(symType)
				} else {
					t = "<unknown>"
				}
			}
			return &ir.Ident{Name: symbolName(sym), Type: t, Location: loc}
		}
		return &ir.InvalidExpr{Message: "unresolved qualified identifier: " + node.Module.Name + "::" + node.Name.Name, Type: "<invalid>", Location: loc}

	case *ast.UnaryExpr:
		arg := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" {
			t = arg.TypeText()
			if node.Op == "!" {
				t = "bool"
			}
		}
		return &ir.Unary{Op: node.Op, Arg: arg, Type: t, Location: loc}

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
		return &ir.Binary{Op: node.Op, Left: left, Right: right, Type: t, Location: loc}

	case *ast.CallExpr:
		if selector, ok := node.Callee.(*ast.SelectorExpr); ok && selector != nil {
			return lowerSelectorMethodCall(ctx, module, scope, selector, node)
		}
		calleeExpr := lowerASTExpr(ctx, module, scope, node.Callee, nil)
		args := make([]ir.Expr, 0, len(node.Args))
		var fnType *typeinfo.FuncType
		if resolved := exprResolvedType(module, node.Callee); resolved != nil {
			fnType, _ = typeinfo.Underlying(resolved).(*typeinfo.FuncType)
		}
		for _, arg := range node.Args {
			var paramExpected typeinfo.Type
			if fnType != nil && len(args) < len(fnType.Params) {
				paramExpected = fnType.Params[len(args)]
			}
			args = append(args, lowerASTExpr(ctx, module, scope, arg, paramExpected))
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
					t = loweredTypeText(fnType.Return)
				}
			}
		}
		return &ir.Call{Callee: calleeExpr, Args: args, Type: t, Location: loc}

	case *ast.AsExpr:
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" {
			t = loweredTypeText(typeinfo.TypeFromSyntax(node.TypeExpr))
		}
		subExpr := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		return &ir.Cast{Expr: subExpr, Type: t, Location: loc}

	case *ast.SelectorExpr:
		return lowerSelectorExpr(ctx, module, scope, node)

	case *ast.StructLit:
		return lowerStructLiteralExpr(ctx, module, scope, node)

	default:
		return &ir.InvalidExpr{Message: "unsupported expression", Type: "<invalid>", Location: loc}
	}
}

func lowerSelectorMethodCall(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, selector *ast.SelectorExpr, call *ast.CallExpr) ir.Expr {
	if module == nil || selector == nil || selector.Expr == nil || selector.Name == nil {
		return &ir.InvalidExpr{Message: "invalid selector call", Type: "<invalid>"}
	}
	baseType := exprResolvedType(module, selector.Expr)
	if iface, slot, ok := lookupInterfaceMethod(baseType, selector.Name.Name); ok {
		args := make([]ir.Expr, 0, len(call.Args))
		for i, arg := range call.Args {
			var argExpected typeinfo.Type
			if i+1 < len(iface.Params) {
				argExpected = iface.Params[i+1].Type
			}
			args = append(args, lowerASTExpr(ctx, module, scope, arg, argExpected))
		}
		return &ir.InterfaceCall{
			Base:     lowerASTExpr(ctx, module, scope, selector.Expr, nil),
			Slot:     slot,
			Args:     args,
			Type:     loweredTypeText(iface.Return),
			Location: ast.LocOf(call),
		}
	}
	methodOwnerKey, methodSym, fnType := lookupLoweredMethod(module, baseType, selector.Name.Name)
	if methodSym == nil || fnType == nil {
		return &ir.InvalidExpr{Message: "unsupported selector call lowering", Type: "<invalid>"}
	}
	baseExpr := lowerASTExpr(ctx, module, scope, selector.Expr, nil)
	if receiverNeedsAddress(module, scope, fnType, baseType, selector.Expr) {
		baseExpr = &ir.AddrOf{
			Expr:     baseExpr,
			Type:     loweredTypeText(fnType.Params[0]),
			Location: ast.LocOf(selector.Expr),
		}
	}
	args := make([]ir.Expr, 0, len(call.Args)+1)
	args = append(args, baseExpr)
	for i, arg := range call.Args {
		var argExpected typeinfo.Type
		if i+1 < len(fnType.Params) {
			argExpected = fnType.Params[i+1]
		}
		args = append(args, lowerASTExpr(ctx, module, scope, arg, argExpected))
	}
	return &ir.Call{
		Callee: &ir.Ident{
			Name:     methodSymbolRefName(methodOwnerKey, methodSym),
			Type:     loweredTypeText(fnType),
			Location: ast.LocOf(selector.Name),
		},
		Args:     args,
		Type:     loweredTypeText(fnType.Return),
		Location: ast.LocOf(call),
	}
}

func receiverNeedsAddress(module *project.Module, scope *table.Scope, fnType *typeinfo.FuncType, baseType typeinfo.Type, receiver ast.Expr) bool {
	if scope == nil || fnType == nil || len(fnType.Params) == 0 || receiver == nil {
		return false
	}
	ptrType, ok := fnType.Params[0].(*typeinfo.RawPtrType)
	if !ok || ptrType == nil || ptrType.Target == nil {
		return false
	}
	if !typeinfo.SameType(ptrType.Target, baseType) {
		return false
	}
	return isAddressableExpr(module, scope, receiver)
}

func lowerSelectorExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, selector *ast.SelectorExpr) ir.Expr {
	if module == nil || selector == nil || selector.Expr == nil || selector.Name == nil {
		return &ir.InvalidExpr{Message: "invalid selector", Type: "<invalid>"}
	}
	baseType := exprResolvedType(module, selector.Expr)
	if fieldType, fieldIndex, ok := lookupStructField(baseType, selector.Name.Name); ok {
		_, throughPtr := baseType.(*typeinfo.RawPtrType)
		return &ir.Field{
			Base:       lowerASTExpr(ctx, module, scope, selector.Expr, nil),
			Index:      fieldIndex,
			ThroughPtr: throughPtr,
			Type:       loweredTypeText(fieldType),
			Location:   ast.LocOf(selector),
		}
	}
	return &ir.InvalidExpr{Message: "selector lowering not implemented", Type: "<invalid>", Location: ast.LocOf(selector)}
}

func lowerStructLiteralExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, node *ast.StructLit) ir.Expr {
	if module == nil || node == nil {
		return &ir.InvalidExpr{Message: "invalid struct literal", Type: "<invalid>", Location: ast.LocOf(node)}
	}
	resolved := exprResolvedType(module, node)
	strct, ok := loweredRuntimeType(resolved, nil).(*typeinfo.StructType)
	if !ok || strct == nil {
		return &ir.InvalidExpr{Message: "struct literal type missing", Type: "<invalid>", Location: ast.LocOf(node)}
	}
	fieldsByName := make(map[string]ast.Expr, len(node.Fields))
	for _, field := range node.Fields {
		if field.Name == nil || field.Value == nil {
			continue
		}
		fieldsByName[field.Name.Name] = field.Value
	}
	values := make([]ir.Expr, 0, len(strct.Fields))
	for _, field := range strct.Fields {
		value, ok := fieldsByName[field.Name]
		if !ok {
			return &ir.InvalidExpr{Message: "struct literal field missing during lowering", Type: "<invalid>", Location: ast.LocOf(node)}
		}
		values = append(values, lowerASTExpr(ctx, module, scope, value, field.Type))
	}
	return &ir.StructLit{
		Fields:   values,
		Type:     loweredTypeText(resolved),
		Location: ast.LocOf(node),
	}
}

func maybeLowerInterfaceExpr(ctx *project.CompilerContext, module *project.Module, scope *table.Scope, expr ast.Expr, expectedType typeinfo.Type) ir.Expr {
	if expectedType == nil {
		return nil
	}
	iface, ok := loweredRuntimeType(expectedType, nil).(*typeinfo.InterfaceType)
	if !ok || iface == nil {
		return nil
	}
	resolved := exprResolvedType(module, expr)
	if resolved == nil {
		return nil
	}
	if _, ok := loweredRuntimeType(resolved, nil).(*typeinfo.InterfaceType); ok {
		return nil
	}
	slots := make([]ir.InterfaceSlot, 0, len(iface.Methods))
	for _, method := range iface.Methods {
		actualType, methodSym, ownerKey, ok := lookupInterfaceImplementation(module, resolved, method.Name)
		if !ok || actualType == nil || methodSym == nil {
			return &ir.InvalidExpr{Message: "missing interface method implementation", Type: "<invalid>", Location: ast.LocOf(expr)}
		}
		slotType := interfaceSlotTypeText(method)
		if slotType == "" {
			return &ir.InvalidExpr{Message: "unsupported interface method shape", Type: "<invalid>", Location: ast.LocOf(expr)}
		}
		slots = append(slots, ir.InterfaceSlot{
			InterfaceType: loweredTypeText(expectedType),
			MethodName:    method.Name,
			SlotType:      slotType,
			FuncName:      methodSymbolRefName(ownerKey, methodSym),
			FuncType:      loweredTypeText(actualType),
			DataType:      loweredTypeText(resolved),
		})
	}
	return &ir.InterfaceMake{
		Value:    lowerASTExpr(ctx, module, scope, expr, nil),
		Slots:    slots,
		Type:     loweredTypeText(expectedType),
		Location: ast.LocOf(expr),
	}
}

func lookupInterfaceImplementation(module *project.Module, concrete typeinfo.Type, name string) (*typeinfo.FuncType, *symbols.Symbol, string, bool) {
	owner := concrete
	if ptr, ok := concrete.(*typeinfo.RawPtrType); ok && ptr != nil && ptr.Target != nil {
		owner = ptr.Target
	}
	ownerKey, sym, fnType := lookupLoweredMethod(module, owner, name)
	if sym == nil || fnType == nil {
		return nil, nil, "", false
	}
	return fnType, sym, ownerKey, true
}

func lookupInterfaceMethod(baseType typeinfo.Type, name string) (*typeinfo.Method, int, bool) {
	iface, ok := loweredRuntimeType(baseType, nil).(*typeinfo.InterfaceType)
	if !ok || iface == nil {
		return nil, -1, false
	}
	for i := range iface.Methods {
		if iface.Methods[i].Name == name {
			return &iface.Methods[i], i, true
		}
	}
	return nil, -1, false
}

func interfaceSlotTypeText(method typeinfo.Method) string {
	var b strings.Builder
	b.WriteString("fn(^u8")
	for i, param := range method.Params {
		if i == 0 {
			continue
		}
		text, ok := lowerInterfaceSlotValueType(param.Type)
		if !ok {
			return ""
		}
		if text == "" {
			return ""
		}
		b.WriteString(", ")
		b.WriteString(text)
	}
	b.WriteString(")")
	text, ok := lowerInterfaceSlotValueType(method.Return)
	if !ok {
		return ""
	}
	if text != "" {
		b.WriteString(" -> ")
		b.WriteString(text)
	}
	return b.String()
}

func lowerInterfaceSlotValueType(t typeinfo.Type) (string, bool) {
	if t == nil {
		return "", true
	}
	runtimeType := loweredRuntimeType(t, nil)
	if _, ok := runtimeType.(*typeinfo.InterfaceType); ok {
		return loweredTypeText(runtimeType), true
	}
	if typeinfo.ContainsAbstractSelf(runtimeType) {
		return "", false
	}
	text := loweredTypeText(runtimeType)
	if text == "" {
		return "", false
	}
	return text, true
}

func exprResolvedType(module *project.Module, expr ast.Expr) typeinfo.Type {
	if module == nil || module.Semantics == nil || expr == nil {
		return nil
	}
	return module.Semantics.ExprTypes[expr.ID()]
}

func lookupLoweredMethod(module *project.Module, baseType typeinfo.Type, name string) (string, *symbols.Symbol, *typeinfo.FuncType) {
	if module == nil || module.Semantics == nil || baseType == nil || name == "" {
		return "", nil, nil
	}
	for _, key := range loweredMethodLookupKeys(baseType) {
		for _, method := range module.Semantics.MethodSets[key] {
			if method == nil || method.Name != name {
				continue
			}
			typ, ok := symbols.GetSymbolType(method)
			if !ok {
				continue
			}
			fnType, ok := typ.(*typeinfo.FuncType)
			if ok && fnType != nil {
				return key, method, fnType
			}
		}
	}
	return "", nil, nil
}

func lookupStructField(baseType typeinfo.Type, name string) (typeinfo.Type, int, bool) {
	if ptr, ok := baseType.(*typeinfo.RawPtrType); ok && ptr != nil && ptr.Target != nil {
		baseType = ptr.Target
	}
	strct, ok := loweredRuntimeType(baseType, nil).(*typeinfo.StructType)
	if !ok || strct == nil {
		return nil, -1, false
	}
	for i, field := range strct.Fields {
		if field.Name == name {
			return field.Type, i, true
		}
	}
	return nil, -1, false
}

func loweredMethodLookupKeys(baseType typeinfo.Type) []string {
	keys := make([]string, 0, 4)
	appendKey := func(typ typeinfo.Type) {
		if typ == nil {
			return
		}
		key := typeinfo.TypeText(typ)
		if key == "" {
			return
		}
		if slices.Contains(keys, key) {
			return
		}
		keys = append(keys, key)
	}
	appendKey(baseType)
	if underlying := typeinfo.Underlying(baseType); underlying != baseType {
		appendKey(underlying)
	}
	if ptr, ok := baseType.(*typeinfo.RawPtrType); ok && ptr != nil && ptr.Target != nil {
		appendKey(ptr.Target)
		if underlying := typeinfo.Underlying(ptr.Target); underlying != ptr.Target {
			appendKey(underlying)
		}
	}
	return keys
}

func methodFunctionName(targetText, methodName string) string {
	var b strings.Builder
	b.WriteString("__impl__")
	b.WriteString(ir.SanitizeSymbolName(targetText))
	b.WriteString("__")
	b.WriteString(methodName)
	return b.String()
}

func methodSymbolRefName(targetText string, sym *symbols.Symbol) string {
	if sym == nil {
		return ""
	}
	return fmt.Sprintf("%s$%d", methodFunctionName(targetText, sym.Name), sym.ID)
}

// lookupScopeResolutionSymbol resolves a ScopeResolution node in two steps:
// 1. Find the imported module by alias in module.Imports.
// 2. Look up the symbol in that module's scope.
// Returns nil if resolution fails.
func lookupScopeResolutionSymbol(ctx *project.CompilerContext, module *project.Module, _ *table.Scope, sr *ast.ScopeResolution) *symbols.Symbol {
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
func lowerNumberLit(node *ast.NumberLit, expectedType string, loc *source.Location) ir.Expr {
	if node == nil {
		return &ir.InvalidExpr{Message: "nil number literal", Type: "<invalid>"}
	}
	if expectedType == "" || expectedType == "<invalid>" || expectedType == "<unknown>" {
		// No expected type — use language default.
		if numeric.IsFloat(node.Value) {
			return &ir.FloatLit{Value: node.Value, Type: typeinfo.TypeText(typeinfo.DefaultNumberType(node.Value)), Location: loc}
		}
		return &ir.IntLit{Value: node.Value, Type: typeinfo.TypeText(typeinfo.DefaultNumberType(node.Value)), Location: loc}
	}
	if ir.IsFloatType(expectedType) {
		v := node.Value
		if !numeric.IsFloat(v) {
			// Convert integer text to float text for LLVM IR.
			if iv, err := numeric.StringToBigInt(v); err == nil {
				v = iv.String() + ".0"
			}
		}
		return &ir.FloatLit{Value: v, Type: expectedType, Location: loc}
	}
	return &ir.IntLit{Value: node.Value, Type: expectedType, Location: loc}
}

func symbolName(sym *symbols.Symbol) string {
	if sym == nil {
		return ""
	}
	return fmt.Sprintf("%s$%d", sym.Name, sym.ID)
}

func shouldDiscardBindingValue(module *project.Module, symID symbols.SymbolID) bool {
	if module == nil || module.Semantics == nil {
		return false
	}
	_, ok := module.Semantics.DiscardBindingValue[symID]
	return ok
}

func loweredTypeText(t typeinfo.Type) string {
	if t == nil {
		return ""
	}
	return typeinfo.TypeText(loweredRuntimeType(t, nil))
}

func loweredReturnTypeText(t typeinfo.Type) string {
	if t == nil {
		return "void"
	}
	return loweredTypeText(t)
}

func loweredRuntimeType(t typeinfo.Type, seen map[*typeinfo.DefinedType]struct{}) typeinfo.Type {
	if seen == nil {
		seen = make(map[*typeinfo.DefinedType]struct{})
	}
	if t == nil {
		return nil
	}
	t = typeinfo.ResolveTypeWithScope(currentModuleScope, t)
	switch typ := t.(type) {
	case *typeinfo.DefinedType:
		if typ == nil {
			return nil
		}
		if _, ok := seen[typ]; ok {
			// Stop self-recursive expansion once shell already seen.
			return &typeinfo.NamedType{Name: typ.Name}
		}
		seen[typ] = struct{}{}
		defer delete(seen, typ)
		return loweredRuntimeType(typ.Underlying, seen)
	case *typeinfo.RawPtrType:
		if typ == nil {
			return nil
		}
		return &typeinfo.RawPtrType{Mutable: typ.Mutable, Target: loweredRuntimeType(typ.Target, seen)}
	case *typeinfo.StructType:
		if typ == nil {
			return nil
		}
		fields := make([]typeinfo.Field, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			fields = append(fields, typeinfo.Field{Name: field.Name, Type: loweredRuntimeType(field.Type, seen)})
		}
		return &typeinfo.StructType{Fields: fields}
	case *typeinfo.InterfaceType:
		if typ == nil {
			return nil
		}
		methods := make([]typeinfo.Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]typeinfo.Field, 0, len(method.Params))
			for _, param := range method.Params {
				params = append(params, typeinfo.Field{
					Name: param.Name,
					Type: loweredRuntimeType(param.Type, seen),
				})
			}
			methods = append(methods, typeinfo.Method{
				Name:   method.Name,
				Params: params,
				Return: loweredRuntimeType(method.Return, seen),
			})
		}
		return &typeinfo.InterfaceType{Methods: methods}
	case *typeinfo.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]typeinfo.Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, loweredRuntimeType(param, seen))
		}
		return &typeinfo.FuncType{Params: params, Return: loweredRuntimeType(typ.Return, seen)}
	default:
		return typeinfo.Underlying(t)
	}
}
