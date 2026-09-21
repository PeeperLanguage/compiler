package lower

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/typelower"
	"compiler/internal/module"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func maybeLowerInterfaceExpr(ctx *lowering, module *module.Module, scope *symbols.Scope, expr ast.Expr, expectedType typeinfo.Type) ir.Expr {
	if expectedType == nil {
		return nil
	}
	expectedRuntime := typeinfo.Underlying(expectedType)
	iface, ok := typeinfo.InterfaceTypeOf(expectedRuntime)
	if !ok {
		return nil
	}
	resolved := exprResolvedType(module, expr)
	if resolved == nil {
		return nil
	}
	resolvedRuntime := typeinfo.Underlying(resolved)
	if _, ok := typeinfo.InterfaceTypeOf(resolvedRuntime); ok {
		return nil
	}
	dataType := resolvedRuntime
	if target, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(resolvedRuntime)); ok {
		dataType = target
	} else if target, ok := typeinfo.PointerTarget(typeinfo.Underlying(resolvedRuntime)); ok {
		dataType = target
	}
	interfaceType := typelower.Type(ctx.types, ctx.diagnostics, expectedType)
	if interfaceType == ir.InvalidType {
		return &ir.InvalidExpr{Message: "invalid interface runtime type", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
	}
	slots := make([]ir.InterfaceSlot, 0, len(iface.Methods))
	implementations := module.Typechecking.InterfaceImplementations(expr.ID())
	if len(implementations) != len(iface.Methods) {
		return &ir.InvalidExpr{Message: "missing interface implementation evidence", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
	}
	for index, method := range iface.Methods {
		implementation := implementations[index]
		if implementation.Symbol == nil || implementation.Symbol.Name != method.Name || implementation.CallableType == nil {
			return &ir.InvalidExpr{Message: "missing interface method implementation", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
		}
		loweredMethod, ok := ctx.types.InterfaceMethod(interfaceType, index)
		if !ok || loweredMethod.Name != method.Name || loweredMethod.SlotType == ir.InvalidType {
			return &ir.InvalidExpr{Message: "missing lowered interface slot evidence", Type: ir.InvalidType, SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)}}
		}
		slots = append(slots, ir.InterfaceSlot{
			InterfaceType: interfaceType,
			MethodName:    method.Name,
			SlotType:      loweredMethod.SlotType,
			FuncName:      symbolName(module, implementation.Symbol),
			FuncType:      typelower.Type(ctx.types, ctx.diagnostics, implementation.CallableType),
			DataType:      typelower.Type(ctx.types, ctx.diagnostics, dataType),
		})
	}
	return &ir.InterfaceMake{
		Value:      lowerASTExpr(ctx, module, scope, expr, nil),
		Slots:      slots,
		Type:       interfaceType,
		SourceInfo: ir.SourceInfo{Location: ast.LocOf(expr)},
	}
}

func lookupInterfaceMethod(module *module.Module, baseType typeinfo.Type, name string) (*typeinfo.Method, int, bool) {
	iface, ok := typeinfo.InterfaceTypeOf(baseType)
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

// exprResolvedType reads typechecker output from semantic cache.
// Lowering consumes that result; it should not re-infer expression types.
