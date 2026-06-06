package hir_lower

import (
	"fmt"
	"strings"

	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/utils/numeric"
)

var currentModuleScope *table.Scope

func GenerateHIR(ctx *context.CompilerContext, module *context.Module) *hir.Module {
	if module == nil {
		return nil
	}
	currentModuleScope = module.ModuleScope
	defer func() {
		currentModuleScope = nil
	}()
	out := &hir.Module{
		Name:    module.ImportPath,
		Externs: make([]hir.Extern, 0),
		Funcs:   make([]*hir.Function, 0),
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
					params = append(params, ir.Param{Name: name, Type: loweredTypeText(paramType)})
				}
				returnType := typeinfo.TypeFromSyntax(fn.ReturnType)
				if resolvedFnType != nil && resolvedFnType.Return != nil {
					returnType = resolvedFnType.Return
				}
				out.Externs = append(out.Externs, hir.Extern{
					Name:       sym.Name,
					Params:     params,
					ReturnType: loweredTypeText(returnType),
				})
			} else {
				hirFn := lowerASTFunction(ctx, module, sym, fn)
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

func lowerImplDecl(ctx *context.CompilerContext, module *context.Module, out *hir.Module, decl *ast.ImplDecl) {
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
			fnType, _ := symbolType(sym)
			resolvedFnType, _ := fnType.(*typeinfo.FuncType)
			params := make([]ir.Param, 0, len(method.Params))
			for i, param := range method.Params {
				name := ""
				if param.Name != nil {
					name = param.Name.Name
				}
				paramType := typeinfo.TypeFromSyntax(param.Type)
				if resolvedFnType != nil && i < len(resolvedFnType.Params) && resolvedFnType.Params[i] != nil {
					paramType = resolvedFnType.Params[i]
				}
				params = append(params, ir.Param{Name: name, Type: loweredTypeText(paramType)})
			}
			returnType := typeinfo.TypeFromSyntax(method.ReturnType)
			if resolvedFnType != nil && resolvedFnType.Return != nil {
				returnType = resolvedFnType.Return
			}
			out.Externs = append(out.Externs, hir.Extern{
				Name:       methodFunctionName(targetText, method.Name.Name),
				Params:     params,
				ReturnType: loweredTypeText(returnType),
			})
			continue
		}
		hirFn := lowerASTFunctionNamed(ctx, module, sym, method, methodFunctionName(targetText, method.Name.Name))
		if hirFn != nil {
			out.Funcs = append(out.Funcs, hirFn)
		}
	}
}

func lowerASTFunction(ctx *context.CompilerContext, module *context.Module, sym *symbols.Symbol, fn *ast.FnDecl) *hir.Function {
	return lowerASTFunctionNamed(ctx, module, sym, fn, sym.Name)
}

func lowerASTFunctionNamed(ctx *context.CompilerContext, module *context.Module, sym *symbols.Symbol, fn *ast.FnDecl, emittedName string) *hir.Function {
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
	retTypeStr := loweredTypeText(retType)
	hirFn := &hir.Function{
		Name:       emittedName,
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
		hirFn.Params = append(hirFn.Params, ir.Param{Name: name, Type: loweredTypeText(paramType)})
	}
	appendBlock(module, funcScope, hirFn.Body, fn.Body, retType, ctx)
	return hirFn
}

func appendBlock(module *context.Module, parentScope *table.Scope, out *hir.Block, block *ast.BlockStmt, returnType typeinfo.Type, ctx *context.CompilerContext) {
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

func appendStmt(module *context.Module, scope *table.Scope, out *hir.Block, stmt ast.Stmt, returnType typeinfo.Type, ctx *context.CompilerContext) {
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
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			valueExpr = lowerASTExpr(ctx, module, scope, node.Value, sym.Type)
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
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			valueExpr = lowerASTExpr(ctx, module, scope, node.Value, sym.Type)
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(sym), Constant: true, Value: valueExpr, Location: node.Loc()})

	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if node.Cond != nil {
			condExpr = lowerASTExpr(ctx, module, scope, node.Cond, &typeinfo.BoolType{})
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
		valueExpr := lowerASTExpr(ctx, module, scope, node.Expr, nil)
		out.Stmts = append(out.Stmts, &hir.ExprStmt{Value: valueExpr, Location: node.Loc()})
	case *ast.AssignStmt:
		if node.Target == nil || node.Value == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "assignment missing target or value", Location: node.Loc()})
			return
		}
		targetExpr := lowerAssignTargetExpr(ctx, module, scope, node.Target)
		targetType := exprResolvedType(module, node.Target)
		valueExpr := lowerASTExpr(ctx, module, scope, node.Value, targetType)
		out.Stmts = append(out.Stmts, &hir.Assign{Target: targetExpr, Value: valueExpr, Location: node.Loc()})
	}
}

func lowerAssignTargetExpr(ctx *context.CompilerContext, module *context.Module, scope *table.Scope, expr ast.Expr) ir.Expr {
	if selector, ok := expr.(*ast.SelectorExpr); ok && selector != nil {
		baseType := exprResolvedType(module, selector.Expr)
		if fieldType, fieldIndex, ok := lookupStructField(baseType, selector.Name.Name); ok {
			if _, throughPtr := baseType.(*typeinfo.RawPtrType); throughPtr {
				return &ir.Field{
					Base:       lowerASTExpr(ctx, module, scope, selector.Expr, nil),
					Index:      fieldIndex,
					ThroughPtr: true,
					Type:       loweredTypeText(fieldType),
				}
			}
			if isAddressableExpr(module, scope, selector.Expr) {
				return &ir.Field{
					Base: &ir.AddrOf{
						Expr: lowerAssignTargetExpr(ctx, module, scope, selector.Expr),
						Type: "^" + loweredTypeText(baseType),
					},
					Index:      fieldIndex,
					ThroughPtr: true,
					Type:       loweredTypeText(fieldType),
				}
			}
		}
	}
	return lowerASTExpr(ctx, module, scope, expr, nil)
}

func isAddressableExpr(module *context.Module, scope *table.Scope, expr ast.Expr) bool {
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

func lowerElse(module *context.Module, scope *table.Scope, stmt ast.Stmt, returnType typeinfo.Type, ctx *context.CompilerContext) hir.Stmt {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Loc()}
		appendBlock(module, scope, block, node, returnType, ctx)
		return block
	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if node.Cond != nil {
			condExpr = lowerASTExpr(ctx, module, scope, node.Cond, &typeinfo.BoolType{})
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
func lowerASTExpr(ctx *context.CompilerContext, module *context.Module, scope *table.Scope, expr ast.Expr, expectedType typeinfo.Type) ir.Expr {
	if expr == nil {
		return &ir.InvalidExpr{Message: "nil expression", Type: "<invalid>"}
	}

	// Fetch canonical type from the typechecker side-table when available.
	resolvedTypeStr := ""
	if module != nil && module.Semantics != nil {
		if t, ok := module.Semantics.ExprTypes[expr.ID()]; ok && t != nil {
			resolvedTypeStr = loweredTypeText(t)
		}
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
		return &ir.Call{Callee: calleeExpr, Args: args, Type: t}

	case *ast.AsExpr:
		t := resolvedTypeStr
		if t == "" || t == "<invalid>" {
			t = loweredTypeText(typeinfo.TypeFromSyntax(node.TypeExpr))
		}
		subExpr := lowerASTExpr(ctx, module, scope, node.Expr, expectedType)
		return &ir.Cast{Expr: subExpr, Type: t}

	case *ast.SelectorExpr:
		return lowerSelectorExpr(ctx, module, scope, node)

	case *ast.StructLit:
		return lowerStructLiteralExpr(ctx, module, scope, node)

	default:
		return &ir.InvalidExpr{Message: "unsupported expression", Type: "<invalid>"}
	}
}

func lowerSelectorMethodCall(ctx *context.CompilerContext, module *context.Module, scope *table.Scope, selector *ast.SelectorExpr, call *ast.CallExpr) ir.Expr {
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
			Base: lowerASTExpr(ctx, module, scope, selector.Expr, nil),
			Slot: slot,
			Args: args,
			Type: loweredTypeText(iface.Return),
		}
	}
	methodOwnerKey, methodSym, fnType := lookupLoweredMethod(module, baseType, selector.Name.Name)
	if methodSym == nil || fnType == nil {
		return &ir.InvalidExpr{Message: "unsupported selector call lowering", Type: "<invalid>"}
	}
	baseExpr := lowerASTExpr(ctx, module, scope, selector.Expr, nil)
	if receiverNeedsAddress(module, scope, fnType, baseType, selector.Expr) {
		baseExpr = &ir.AddrOf{
			Expr: baseExpr,
			Type: loweredTypeText(fnType.Params[0]),
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
			Name: methodSymbolRefName(methodOwnerKey, methodSym),
			Type: loweredTypeText(fnType),
		},
		Args: args,
		Type: loweredTypeText(fnType.Return),
	}
}

func receiverNeedsAddress(module *context.Module, scope *table.Scope, fnType *typeinfo.FuncType, baseType typeinfo.Type, receiver ast.Expr) bool {
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

func lowerSelectorExpr(ctx *context.CompilerContext, module *context.Module, scope *table.Scope, selector *ast.SelectorExpr) ir.Expr {
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
		}
	}
	return &ir.InvalidExpr{Message: "selector lowering not implemented", Type: "<invalid>"}
}

func lowerStructLiteralExpr(ctx *context.CompilerContext, module *context.Module, scope *table.Scope, node *ast.StructLit) ir.Expr {
	if module == nil || node == nil {
		return &ir.InvalidExpr{Message: "invalid struct literal", Type: "<invalid>"}
	}
	resolved := exprResolvedType(module, node)
	strct, ok := loweredRuntimeType(resolved).(*typeinfo.StructType)
	if !ok || strct == nil {
		return &ir.InvalidExpr{Message: "struct literal type missing", Type: "<invalid>"}
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
			return &ir.InvalidExpr{Message: "struct literal field missing during lowering", Type: "<invalid>"}
		}
		values = append(values, lowerASTExpr(ctx, module, scope, value, field.Type))
	}
	return &ir.StructLit{
		Fields: values,
		Type:   loweredTypeText(resolved),
	}
}

func maybeLowerInterfaceExpr(ctx *context.CompilerContext, module *context.Module, scope *table.Scope, expr ast.Expr, expectedType typeinfo.Type) ir.Expr {
	if expectedType == nil {
		return nil
	}
	iface, ok := loweredRuntimeType(expectedType).(*typeinfo.InterfaceType)
	if !ok || iface == nil {
		return nil
	}
	resolved := exprResolvedType(module, expr)
	if resolved == nil {
		return nil
	}
	if _, ok := loweredRuntimeType(resolved).(*typeinfo.InterfaceType); ok {
		return nil
	}
	slots := make([]ir.InterfaceSlot, 0, len(iface.Methods))
	for _, method := range iface.Methods {
		actualType, methodSym, ownerKey, ok := lookupInterfaceImplementation(module, resolved, method.Name)
		if !ok || actualType == nil || methodSym == nil {
			return &ir.InvalidExpr{Message: "missing interface method implementation", Type: "<invalid>"}
		}
		slotType := interfaceSlotTypeText(method)
		if slotType == "" {
			return &ir.InvalidExpr{Message: "unsupported interface method shape", Type: "<invalid>"}
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
		Value: lowerASTExpr(ctx, module, scope, expr, nil),
		Slots: slots,
		Type:  loweredTypeText(expectedType),
	}
}

func lookupInterfaceImplementation(module *context.Module, concrete typeinfo.Type, name string) (*typeinfo.FuncType, *symbols.Symbol, string, bool) {
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
	iface, ok := loweredRuntimeType(baseType).(*typeinfo.InterfaceType)
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
		if typeinfo.ContainsAbstractSelf(param.Type) {
			return ""
		}
		text := loweredTypeText(param.Type)
		if text == "" {
			return ""
		}
		b.WriteString(", ")
		b.WriteString(text)
	}
	b.WriteString(")")
	if typeinfo.ContainsAbstractSelf(method.Return) {
		return ""
	}
	if ret := loweredTypeText(method.Return); ret != "" {
		b.WriteString(" -> ")
		b.WriteString(ret)
	}
	return b.String()
}

func exprResolvedType(module *context.Module, expr ast.Expr) typeinfo.Type {
	if module == nil || module.Semantics == nil || expr == nil {
		return nil
	}
	return module.Semantics.ExprTypes[expr.ID()]
}

func lookupLoweredMethod(module *context.Module, baseType typeinfo.Type, name string) (string, *symbols.Symbol, *typeinfo.FuncType) {
	if module == nil || module.Semantics == nil || baseType == nil || name == "" {
		return "", nil, nil
	}
	for _, key := range loweredMethodLookupKeys(baseType) {
		for _, method := range module.Semantics.MethodSets[key] {
			if method == nil || method.Name != name {
				continue
			}
			typ, ok := symbolType(method)
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
	strct, ok := loweredRuntimeType(baseType).(*typeinfo.StructType)
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
		for _, existing := range keys {
			if existing == key {
				return
			}
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
		return loweredTypeText(t)
	}
	return "<unknown>"
}

func loweredTypeText(t typeinfo.Type) string {
	if t == nil {
		return ""
	}
	return typeinfo.TypeText(loweredRuntimeType(t))
}

func loweredRuntimeType(t typeinfo.Type) typeinfo.Type {
	if t == nil {
		return nil
	}
	t = resolveTypeWithScope(currentModuleScope, t)
	switch typ := t.(type) {
	case *typeinfo.DefinedType:
		if typ == nil {
			return nil
		}
		return loweredRuntimeType(typ.Underlying)
	case *typeinfo.RawPtrType:
		if typ == nil {
			return nil
		}
		return &typeinfo.RawPtrType{Mutable: typ.Mutable, Target: loweredRuntimeType(typ.Target)}
	case *typeinfo.StructType:
		if typ == nil {
			return nil
		}
		fields := make([]typeinfo.Field, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			fields = append(fields, typeinfo.Field{Name: field.Name, Type: loweredRuntimeType(field.Type)})
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
					Type: loweredRuntimeType(param.Type),
				})
			}
			methods = append(methods, typeinfo.Method{
				Name:   method.Name,
				Params: params,
				Return: loweredRuntimeType(method.Return),
			})
		}
		return &typeinfo.InterfaceType{Methods: methods}
	case *typeinfo.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]typeinfo.Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, loweredRuntimeType(param))
		}
		return &typeinfo.FuncType{Params: params, Return: loweredRuntimeType(typ.Return)}
	default:
		return typeinfo.Underlying(t)
	}
}

func resolveTypeWithScope(scope *table.Scope, t typeinfo.Type) typeinfo.Type {
	if scope == nil || t == nil {
		return t
	}
	if named, ok := t.(*typeinfo.NamedType); ok && named != nil {
		sym, found := scope.Lookup(named.Name)
		if found && sym != nil && sym.Kind == symbols.SymbolType {
			if resolved, ok := symbolType(sym); ok && resolved != nil {
				return resolveTypeWithScope(scope, resolved)
			}
		}
	}
	return t
}
