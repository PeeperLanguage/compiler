package typechecker

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

func (c *checker) assignable(dst, src typeinfo.Type) bool {
	if c == nil {
		return typeinfo.Assignable(dst, src)
	}
	if typeinfo.Assignable(dst, src) {
		return true
	}
	if dstTarget, dstMutable, dstRef := typeinfo.ReferenceTarget(typeinfo.Underlying(dst)); dstRef {
		srcTarget, srcMutable, srcRef := typeinfo.ReferenceTarget(typeinfo.Underlying(src))
		iface, interfaceRef := typeinfo.InterfaceTypeOf(dstTarget)
		if allowImplicitInterfaceConversion && srcRef && interfaceRef && (!dstMutable || srcMutable) {
			return c.satisfiesInterface(iface, srcTarget)
		}
	}
	if dstTarget, dstOwned := typeinfo.PointerTarget(typeinfo.Underlying(dst)); dstOwned {
		srcTarget, srcOwned := typeinfo.PointerTarget(typeinfo.Underlying(src))
		iface, interfaceOwned := typeinfo.InterfaceTypeOf(dstTarget)
		if allowImplicitInterfaceConversion && srcOwned && interfaceOwned {
			return c.satisfiesInterface(iface, srcTarget)
		}
	}
	return false
}

func (c *checker) satisfiesInterface(iface *typeinfo.InterfaceType, src typeinfo.Type) bool {
	if c == nil || iface == nil || src == nil {
		return false
	}
	owner := c.interfaceImplementorType(src)
	if owner == nil {
		return false
	}
	for _, required := range iface.Methods {
		requiredType := typeinfo.ReplaceAbstractSelf(required.CallableType(), owner)
		actualType, _, ok := c.lookupMethodType(owner, required.Name)
		if !ok || actualType == nil {
			return false
		}
		if !typeinfo.SameType(requiredType, actualType) {
			return false
		}
		fnType, ok := requiredType.(*typeinfo.FuncType)
		if !ok || fnType == nil || len(fnType.Params) == 0 {
			return false
		}
		receiver := fnType.Params[0]
		if _, _, referenceReceiver := typeinfo.ReferenceTarget(typeinfo.Underlying(receiver)); referenceReceiver {
			if !isValidReceiverType(receiver, src) {
				return false
			}
		} else if !typeinfo.Assignable(receiver, src) {
			return false
		}
	}
	return true
}

// missingInterfaceMethods returns names of interface methods not satisfied by src.
func (c *checker) missingInterfaceMethods(iface *typeinfo.InterfaceType, src typeinfo.Type) []string {
	if c == nil || iface == nil || src == nil {
		return nil
	}
	owner := c.interfaceImplementorType(src)
	if owner == nil {
		names := make([]string, len(iface.Methods))
		for i, m := range iface.Methods {
			names[i] = m.Name
		}
		return names
	}
	var missing []string
	for _, required := range iface.Methods {
		actualType, _, ok := c.lookupMethodType(owner, required.Name)
		if !ok || actualType == nil {
			missing = append(missing, required.Name)
		}
	}
	return missing
}

func (c *checker) addInterfaceHint(d *diagnostics.Diagnostic, dst, src typeinfo.Type) {
	iface, ok := typeinfo.InterfaceTypeOf(dst)
	if !ok || iface == nil {
		return
	}
	if missing := c.missingInterfaceMethods(iface, src); len(missing) > 0 {
		d.WithHelp(fmt.Sprintf("missing methods: %s", strings.Join(missing, ", ")))
	}
}

func (c *checker) interfaceImplementorType(src typeinfo.Type) typeinfo.Type {
	if src == nil {
		return nil
	}
	if target, ok := typeinfo.PointerTarget(src); ok {
		return target
	}
	if target, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(src)); ok {
		return target
	}
	return src
}

func isValidReceiverType(paramType, selfType typeinfo.Type) bool {
	if paramType == nil || selfType == nil {
		return false
	}
	if typeinfo.SameType(paramType, selfType) {
		return true
	}
	target, ok := typeinfo.PointerTarget(paramType)
	if ok {
		return typeinfo.SameType(target, selfType)
	}
	target, _, ok = typeinfo.ReferenceTarget(typeinfo.Underlying(paramType))
	return ok && typeinfo.SameType(target, selfType)
}

func (c *checker) matchesReceiverTarget(target, arg typeinfo.Type) bool {
	if c == nil || target == nil || arg == nil {
		return false
	}
	return typeinfo.SameType(target, arg) || c.assignable(target, arg) || c.assignable(arg, target)
}

func (c *checker) lookupMethodType(baseType typeinfo.Type, name string) (typeinfo.Type, *symbols.Symbol, bool) {
	if c == nil || c.module == nil || c.module.Semantics == nil {
		return nil, nil, false
	}
	if iface, ok := typeinfo.InterfaceTypeOf(baseType); ok {
		for _, method := range iface.Methods {
			if method.Name != name {
				continue
			}
			return c.boundInterfaceMethodType(method, baseType), nil, true
		}
	}
	if intrinsic, ok := intrinsics.Symbol(baseType, name, c.ctx.Target); ok {
		return intrinsic.Type, intrinsic, true
	}
	for _, key := range typeinfo.GetMethodLookupKeys(baseType) {
		methods := c.module.Semantics.MethodSets[key]
		for _, method := range methods {
			if method == nil || method.Name != name {
				continue
			}
			typ, ok := symbols.GetSymbolType(method)
			if ok && typ != nil {
				return typ, method, true
			}
		}
	}
	return nil, nil, false
}

// availableMethods returns the names of all methods defined on baseType.
func (c *checker) availableMethods(baseType typeinfo.Type) []string {
	if c == nil || c.module == nil || c.module.Semantics == nil {
		return nil
	}
	var names []string
	for _, method := range intrinsics.Symbols(baseType, c.ctx.Target) {
		if method != nil {
			names = append(names, method.Name)
		}
	}
	if iface, ok := typeinfo.InterfaceTypeOf(baseType); ok {
		for _, m := range iface.Methods {
			names = append(names, m.Name)
		}
	}
	for _, key := range typeinfo.GetMethodLookupKeys(baseType) {
		for _, method := range c.module.Semantics.MethodSets[key] {
			if method != nil {
				names = append(names, method.Name)
			}
		}
	}
	return names
}

// availableFields returns the names of all fields in a struct type.
func availableFields(t typeinfo.Type) []string {
	strct, ok := typeinfo.Underlying(t).(*typeinfo.StructType)
	if !ok || strct == nil {
		return nil
	}
	names := make([]string, len(strct.Fields))
	for i, f := range strct.Fields {
		names[i] = f.Name
	}
	return names
}

func (c *checker) boundInterfaceMethodType(method typeinfo.Method, receiverType typeinfo.Type) typeinfo.Type {
	selfType := receiverType
	if target, _, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(receiverType)); ok {
		selfType = target
	}
	fnType, _ := typeinfo.ReplaceAbstractSelf(method.CallableType(), selfType).(*typeinfo.FuncType)
	if fnType == nil || len(fnType.Params) == 0 {
		return fnType
	}
	if _, _, referenceReceiver := typeinfo.ReferenceTarget(typeinfo.Underlying(fnType.Params[0])); !referenceReceiver {
		if _, _, borrowedCarrier := typeinfo.ReferenceTarget(typeinfo.Underlying(receiverType)); !borrowedCarrier {
			fnType.Params[0] = receiverType
		}
	}
	return fnType
}

func (c *checker) mutableAddressableExpr(scope *table.Scope, expr ast.Expr) (bool, typeinfo.Type) {
	if c == nil {
		return false, nil
	}
	return place.MutableAddressable(scope, expr, func(e ast.Expr) typeinfo.Type {
		return c.typeExpr(scope, e, nil)
	}, c.expandedDefaultBinding)
}

func (c *checker) mutableReceiverDiagnostic(scope *table.Scope, expr ast.Expr) (ast.Node, string, bool) {
	if c == nil || scope == nil || expr == nil {
		return nil, "", false
	}
	curr := expr
	for {
		if sel, ok := curr.(*ast.SelectorExpr); ok && sel != nil {
			curr = sel.Expr
		} else {
			break
		}
	}
	ident, ok := curr.(*ast.Ident)
	if !ok || ident == nil {
		return nil, "", false
	}
	sym, found := scope.Lookup(ident.Name)
	if !found || sym == nil {
		return nil, "", false
	}
	switch sym.Kind {
	case symbols.SymbolConst:
		return expr, "mutable receiver method requires a mutable binding; `" + ident.Name + "` is const", true
	case symbols.SymbolVar, symbols.SymbolParam:
		if !sym.IsMutable() {
			return expr, "mutable receiver method requires a mutable binding; `" + ident.Name + "` is immutable", true
		}
	}
	return nil, "", false
}

// qualifiedScopeType resolves the semantic type of a `module::symbol`
// expression. Imported values such as functions must keep their bound type so
// call analysis can derive argument and return types from the same canonical
// symbol state used elsewhere in the pipeline.
func (c *checker) qualifiedScopeType(node *ast.ScopeResolution) typeinfo.Type {
	if c == nil || node == nil {
		return &typeinfo.InvalidType{}
	}
	var sym *symbols.Symbol
	if c.module != nil && c.module.Semantics != nil {
		sym = c.module.Semantics.ResolvedSymbols[node.ID()]
	}
	if sym == nil {
		resolved, ok := project.LookupImportedSymbol(c.ctx, c.module, node.Module.Name, node.Name.Name)
		if !ok || resolved.Symbol == nil {
			return &typeinfo.InvalidType{}
		}
		sym = resolved.Symbol
	}
	t, ok := symbols.GetSymbolType(sym)
	if !ok || t == nil {
		return &typeinfo.UnknownType{}
	}
	return t
}
