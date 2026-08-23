package typechecker

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func (c *checker) assignable(dst, src typeinfo.Type, conversion ast.Expr) bool {
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
			implementations, _, ok := c.resolveInterfaceImplementations(iface, srcTarget)
			if ok {
				c.storeInterfaceImplementations(conversion, implementations)
			}
			return ok
		}
	}
	if dstTarget, dstOwned := typeinfo.PointerTarget(typeinfo.Underlying(dst)); dstOwned {
		srcTarget, srcOwned := typeinfo.PointerTarget(typeinfo.Underlying(src))
		iface, interfaceOwned := typeinfo.InterfaceTypeOf(dstTarget)
		if allowImplicitInterfaceConversion && srcOwned && interfaceOwned {
			implementations, _, ok := c.resolveInterfaceImplementations(iface, srcTarget)
			if ok {
				c.storeInterfaceImplementations(conversion, implementations)
			}
			return ok
		}
	}
	return false
}

func (c *checker) resolveInterfaceImplementations(iface *typeinfo.InterfaceType, src typeinfo.Type) ([]project.InterfaceImplementation, []string, bool) {
	if c == nil || iface == nil || src == nil {
		return nil, nil, false
	}
	owner := c.interfaceImplementorType(src)
	if owner == nil {
		missing := make([]string, len(iface.Methods))
		for i, method := range iface.Methods {
			missing[i] = method.Name
		}
		return nil, missing, false
	}
	implementations := make([]project.InterfaceImplementation, 0, len(iface.Methods))
	missing := make([]string, 0)
	for _, required := range iface.Methods {
		requiredType := typeinfo.ReplaceAbstractSelf(required.CallableType(), owner)
		actual, ok := c.lookupDeclaredCallableMember(owner, required.Name)
		actualType, callable := actual.Type.(*typeinfo.FuncType)
		if !ok || actual.Symbol == nil || !callable || actualType == nil || !typeinfo.SameType(requiredType, actualType) {
			missing = append(missing, required.Name)
			continue
		}
		fnType, ok := requiredType.(*typeinfo.FuncType)
		if !ok || fnType == nil || len(fnType.Params) == 0 {
			missing = append(missing, required.Name)
			continue
		}
		receiver := fnType.Params[0]
		if _, _, referenceReceiver := typeinfo.ReferenceTarget(typeinfo.Underlying(receiver)); referenceReceiver {
			if !isValidReceiverType(receiver, src) {
				missing = append(missing, required.Name)
				continue
			}
		} else if !typeinfo.Assignable(receiver, src) {
			missing = append(missing, required.Name)
			continue
		}
		implementations = append(implementations, project.InterfaceImplementation{
			MethodName: required.Name, Symbol: actual.Symbol,
			CallableType: actualType, OwnerKey: actual.OwnerKey,
		})
	}
	return implementations, missing, len(missing) == 0
}

func (c *checker) storeInterfaceImplementations(expr ast.Expr, implementations []project.InterfaceImplementation) {
	if c == nil || c.module == nil || c.module.Semantics == nil || expr == nil {
		return
	}
	c.module.Semantics.InterfaceImplementations[expr.ID()] = implementations
}

func (c *checker) addInterfaceHint(d *diagnostics.Diagnostic, dst, src typeinfo.Type) {
	iface, ok := typeinfo.InterfaceTypeOf(dst)
	if !ok || iface == nil {
		return
	}
	_, missing, _ := c.resolveInterfaceImplementations(iface, src)
	if len(missing) > 0 {
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
	return typeinfo.SameType(target, arg) || c.assignable(target, arg, nil) || c.assignable(arg, target, nil)
}

type callableMember struct {
	Type     typeinfo.Type
	Symbol   *symbols.Symbol
	OwnerKey string
}

func (c *checker) lookupCallableMember(baseType typeinfo.Type, name string) (callableMember, bool) {
	if c == nil || c.module == nil || c.module.Semantics == nil {
		return callableMember{}, false
	}
	if iface, ok := typeinfo.InterfaceTypeOf(baseType); ok {
		for _, method := range iface.Methods {
			if method.Name != name {
				continue
			}
			return callableMember{Type: c.boundInterfaceMethodType(method, baseType)}, true
		}
	}
	return c.lookupDeclaredCallableMember(baseType, name)
}

func (c *checker) lookupDeclaredCallableMember(baseType typeinfo.Type, name string) (callableMember, bool) {
	if c == nil || c.module == nil || c.module.Semantics == nil {
		return callableMember{}, false
	}
	for _, key := range typeinfo.GetMethodLookupKeys(baseType) {
		methods := c.module.Semantics.MethodSets[key]
		for _, method := range methods {
			if method == nil || method.Name != name {
				continue
			}
			typ, ok := symbols.GetSymbolType(method)
			if ok && typ != nil {
				return callableMember{Type: typ, Symbol: method, OwnerKey: key}, true
			}
		}
	}
	return callableMember{}, false
}

// availableMethods returns the names of all methods defined on baseType.
func (c *checker) availableMethods(baseType typeinfo.Type) []string {
	if c == nil || c.module == nil || c.module.Semantics == nil {
		return nil
	}
	var names []string
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

func (c *checker) mutableAddressableExpr(scope *symbols.Scope, expr ast.Expr) (bool, typeinfo.Type) {
	if c == nil {
		return false, nil
	}
	return place.MutableAddressable(scope, expr, func(e ast.Expr) typeinfo.Type {
		return c.typeExpr(scope, e, nil)
	}, c.expandedDefaultBinding)
}

func (c *checker) mutableImplicitArgumentDiagnostic(scope *symbols.Scope, expr ast.Expr) (ast.Node, string, bool) {
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
		return expr, "implicit mutable borrow requires a mutable binding; `" + ident.Name + "` is const", true
	case symbols.SymbolVar, symbols.SymbolParam:
		if !sym.IsMutable() {
			return expr, "implicit mutable borrow requires a mutable binding; `" + ident.Name + "` is immutable", true
		}
	}
	return nil, "", false
}

// qualifiedScopeType resolves the semantic type of a `module::symbol`
// expression. Imported values such as functions must keep their bound type so
// call analysis can derive argument and return types from the same canonical
// symbol state used elsewhere in the pipeline.
func (c *checker) qualifiedScopeType(scope *symbols.Scope, node *ast.ScopeResolution) typeinfo.Type {
	if c == nil || node == nil {
		return &typeinfo.InvalidType{}
	}
	var sym *symbols.Symbol
	if c.module != nil && c.module.Semantics != nil {
		sym = c.module.Semantics.ResolvedSymbols[node.ID()]
	}
	if sym == nil {
		qualifier, member, imported := node.ImportValueMember()
		if !imported {
			return &typeinfo.InvalidType{}
		}
		resolved, ok := project.LookupImportedSymbol(c.ctx, c.module, qualifier.Name, member.Name)
		if !ok || resolved.Symbol == nil {
			return &typeinfo.InvalidType{}
		}
		sym = resolved.Symbol
	}
	if sym.Kind == symbols.SymbolVariant {
		return c.typeVariantConstruction(scope, node, node, nil, false)
	}
	t, ok := symbols.GetSymbolType(sym)
	if !ok || t == nil {
		return &typeinfo.UnknownType{}
	}
	return t
}
