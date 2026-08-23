package lower

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func maybeLowerInterfaceExpr(ctx *project.CompilerContext, module *project.Module, scope *symbols.Scope, expr ast.Expr, expectedType typeinfo.Type) ir.Expr {
	if expectedType == nil {
		return nil
	}
	expectedRuntime := loweredRuntimeType(module, expectedType, nil)
	iface, ok := typeinfo.InterfaceTypeOf(expectedRuntime)
	if !ok {
		return nil
	}
	resolved := exprResolvedType(module, expr)
	if resolved == nil {
		return nil
	}
	resolvedRuntime := loweredRuntimeType(module, resolved, nil)
	if _, ok := typeinfo.InterfaceTypeOf(resolvedRuntime); ok {
		return nil
	}
	dataType := resolvedRuntime
	if target, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(resolvedRuntime)); ok {
		dataType = target
	} else if target, ok := typeinfo.PointerTarget(typeinfo.Underlying(resolvedRuntime)); ok {
		dataType = target
	}
	slots := make([]ir.InterfaceSlot, 0, len(iface.Methods))
	implementations := module.Semantics.InterfaceImplementations[expr.ID()]
	if len(implementations) != len(iface.Methods) {
		return &ir.InvalidExpr{Message: "missing interface implementation evidence", Type: ir.InvalidType, Location: ast.LocOf(expr)}
	}
	for index, method := range iface.Methods {
		implementation := implementations[index]
		if implementation.MethodName != method.Name || implementation.CallableType == nil || implementation.Symbol == nil || implementation.OwnerKey == "" {
			return &ir.InvalidExpr{Message: "missing interface method implementation", Type: ir.InvalidType, Location: ast.LocOf(expr)}
		}
		slotType, ok := interfaceSlotTypeID(ctx, module, method)
		if !ok {
			return &ir.InvalidExpr{Message: "unsupported interface method shape", Type: ir.InvalidType, Location: ast.LocOf(expr)}
		}
		slots = append(slots, ir.InterfaceSlot{
			InterfaceType: loweredTypeID(ctx, module, expectedType),
			MethodName:    method.Name,
			SlotType:      slotType,
			FuncName:      symbolName(module, implementation.Symbol),
			FuncType:      loweredTypeID(ctx, module, implementation.CallableType),
			DataType:      loweredTypeID(ctx, module, dataType),
		})
	}
	return &ir.InterfaceMake{
		Value:    lowerASTExpr(ctx, module, scope, expr, nil),
		Slots:    slots,
		Type:     loweredTypeID(ctx, module, expectedType),
		Location: ast.LocOf(expr),
	}
}

func lookupInterfaceMethod(module *project.Module, baseType typeinfo.Type, name string) (*typeinfo.Method, int, bool) {
	iface, ok := typeinfo.InterfaceTypeOf(loweredRuntimeType(module, baseType, nil))
	if !ok {
		return nil, -1, false
	}
	for i := range iface.Methods {
		if iface.Methods[i].Name == name {
			return &iface.Methods[i], i, true
		}
	}
	return nil, -1, false
}

func interfaceSlotTypeID(ctx *project.CompilerContext, module *project.Module, method typeinfo.Method) (ir.TypeID, bool) {
	params := []ir.TypeID{ctx.Types.Intern(ir.Type{Kind: ir.TypeRawPtr})}
	for i, param := range method.Params {
		if i == 0 {
			continue
		}
		typ, ok := lowerInterfaceSlotValueType(ctx, module, param.Type)
		if !ok {
			return ir.InvalidType, false
		}
		params = append(params, typ)
	}
	returnType, ok := lowerInterfaceSlotValueType(ctx, module, method.Return)
	if !ok {
		return ir.InvalidType, false
	}
	if returnType == ir.InvalidType {
		returnType = ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid})
	}
	return ctx.Types.Intern(ir.Type{Kind: ir.TypeFunction, Params: params, Return: returnType}), true
}

func lowerInterfaceSlotValueType(ctx *project.CompilerContext, module *project.Module, t typeinfo.Type) (ir.TypeID, bool) {
	if t == nil {
		return ctx.Types.Intern(ir.Type{Kind: ir.TypeVoid}), true
	}
	runtimeType := loweredRuntimeType(module, t, nil)
	if _, ok := typeinfo.InterfaceTypeOf(runtimeType); ok {
		return loweredTypeID(ctx, module, runtimeType), true
	}
	if typeinfo.ContainsAbstractSelf(runtimeType) {
		return ir.InvalidType, false
	}
	typ := loweredTypeID(ctx, module, runtimeType)
	if typ == ir.InvalidType {
		return ir.InvalidType, false
	}
	return typ, true
}

// exprResolvedType reads typechecker output from semantic cache.
// Lowering consumes that result; it should not re-infer expression types.
