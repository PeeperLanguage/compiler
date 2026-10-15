package typechecker

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/types"
)

func (c *checker) isAssignable(dst, src types.Type, site ast.Expr) bool {
	if c == nil {
		return types.Assignable(dst, src)
	}
	conversion := types.CheckCompatibility(dst, src)
	if conversion.Compatibility == types.Compatible {
		c.recordImplicitConversion(site, conversion)
		return true
	}
	if dstTarget, dstMutable, dstRef := types.ReferenceTarget(types.Underlying(dst)); dstRef {
		srcTarget, srcMutable, srcRef := types.ReferenceTarget(types.Underlying(src))
		iface, interfaceRef := types.InterfaceTypeOf(dstTarget)
		if allowImplicitInterfaceConversion && srcRef && interfaceRef && (!dstMutable || srcMutable) {
			implementations, _, ok := c.resolveInterfaceImplementations(iface, srcTarget)
			if ok {
				c.storeInterfaceImplementations(site, implementations)
			}
			return ok
		}
	}
	if dstTarget, dstOwned := types.PointerTarget(types.Underlying(dst)); dstOwned {
		srcTarget, srcOwned := types.PointerTarget(types.Underlying(src))
		iface, interfaceOwned := types.InterfaceTypeOf(dstTarget)
		if allowImplicitInterfaceConversion && srcOwned && interfaceOwned {
			implementations, _, ok := c.resolveInterfaceImplementations(iface, srcTarget)
			if ok {
				c.storeInterfaceImplementations(site, implementations)
			}
			return ok
		}
	}
	return false
}

func (c *checker) recordImplicitConversion(expr ast.Expr, conversion types.Conversion) {
	if c == nil || c.module == nil || c.evidence == nil || expr == nil ||
		conversion.Kind == types.ConversionNone || conversion.Kind == types.ConversionRecovery ||
		conversion.Kind == types.ConversionIdentity || conversion.Compatibility != types.Compatible {
		return
	}
	c.evidence.RecordImplicitConversion(expr.ID(), conversion)
}

func (c *checker) resolveInterfaceImplementations(iface *types.InterfaceType, src types.Type) ([]thir.InterfaceImplementation, []string, bool) {
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
	implementations := make([]thir.InterfaceImplementation, 0, len(iface.Methods))
	missing := make([]string, 0)
	for _, required := range iface.Methods {
		fnType := required.CallableTypeFor(owner)
		actual, ok := c.lookupDeclaredCallableMember(owner, required.Name)
		actualType, isCallable := actual.Type.(*types.FuncType)
		if !ok || actual.Symbol == nil || !isCallable || actualType == nil || !types.IsSameType(fnType, actualType) {
			missing = append(missing, required.Name)
			continue
		}
		if len(fnType.Params) == 0 {
			missing = append(missing, required.Name)
			continue
		}
		receiver := fnType.Params[0]
		if _, _, referenceReceiver := types.ReferenceTarget(types.Underlying(receiver)); referenceReceiver {
			if !isValidReceiverType(receiver, src) {
				missing = append(missing, required.Name)
				continue
			}
		} else if !types.Assignable(receiver, src) {
			missing = append(missing, required.Name)
			continue
		}
		implementations = append(implementations, thir.InterfaceImplementation{
			Symbol: actual.Symbol, CallableType: actualType,
		})
	}
	return implementations, missing, len(missing) == 0
}

func (c *checker) storeInterfaceImplementations(expr ast.Expr, implementations []thir.InterfaceImplementation) {
	if c == nil || c.module == nil || c.evidence == nil || expr == nil {
		return
	}
	c.evidence.RecordInterfaceImplementations(expr.ID(), implementations)
}

func (c *checker) addInterfaceHint(d *diagnostics.Diagnostic, dst, src types.Type) {
	iface, ok := types.InterfaceTypeOf(dst)
	if !ok || iface == nil {
		return
	}
	_, missing, _ := c.resolveInterfaceImplementations(iface, src)
	if len(missing) > 0 {
		d.Help(fmt.Sprintf("missing methods: %s", strings.Join(missing, ", ")))
	}
}

func (c *checker) interfaceImplementorType(src types.Type) types.Type {
	if src == nil {
		return nil
	}
	if target, ok := types.PointerTarget(src); ok {
		return target
	}
	if target, _, ok := types.ReferenceTarget(types.Underlying(src)); ok {
		return target
	}
	return src
}

func isValidReceiverType(paramType, selfType types.Type) bool {
	if paramType == nil || selfType == nil {
		return false
	}
	if types.IsSameType(paramType, selfType) {
		return true
	}
	target, ok := types.PointerTarget(paramType)
	if ok {
		return types.IsSameType(target, selfType)
	}
	target, _, ok = types.ReferenceTarget(types.Underlying(paramType))
	return ok && types.IsSameType(target, selfType)
}

func (c *checker) matchesReceiverTarget(target, arg types.Type) bool {
	if c == nil || target == nil || arg == nil {
		return false
	}
	return types.IsSameType(target, arg) || c.isAssignable(target, arg, nil) || c.isAssignable(arg, target, nil)
}

type callableMember struct {
	Type   types.Type
	Symbol *symbols.Symbol
}

func (c *checker) lookupCallableMember(baseType types.Type, name string) (callableMember, bool) {
	if c == nil {
		return callableMember{}, false
	}
	if iface, ok := types.InterfaceTypeOf(baseType); ok {
		for _, method := range iface.Methods {
			if method.Name != name {
				continue
			}
			return callableMember{Type: c.boundInterfaceMethodType(method, baseType)}, true
		}
	}
	return c.lookupDeclaredCallableMember(baseType, name)
}

func (c *checker) lookupDeclaredCallableMember(baseType types.Type, name string) (callableMember, bool) {
	if c == nil || c.module == nil || c.module.SymbolIndex == nil {
		return callableMember{}, false
	}
	for _, method := range c.module.SymbolIndex.Methods(baseType) {
		if method == nil || method.Name != name {
			continue
		}
		typ, ok := symbols.GetSymbolType(method)
		if ok && typ != nil {
			return callableMember{Type: typ, Symbol: method}, true
		}
	}
	return callableMember{}, false
}

// availableMethods returns the names of all methods defined on baseType.
func (c *checker) availableMethods(baseType types.Type) []string {
	if c == nil {
		return nil
	}
	var names []string
	if iface, ok := types.InterfaceTypeOf(baseType); ok {
		for _, m := range iface.Methods {
			names = append(names, m.Name)
		}
	}
	if c.module != nil && c.module.SymbolIndex != nil {
		for _, method := range c.module.SymbolIndex.Methods(baseType) {
			if method != nil {
				names = append(names, method.Name)
			}
		}
	}
	return names
}

// availableFields returns the names of all fields in a struct type.
func availableFields(t types.Type) []string {
	strct, ok := types.Underlying(t).(*types.StructType)
	if !ok || strct == nil {
		return nil
	}
	names := make([]string, len(strct.Fields))
	for i, f := range strct.Fields {
		names[i] = f.Name
	}
	return names
}

func (c *checker) boundInterfaceMethodType(method types.Method, receiverType types.Type) types.Type {
	selfType := receiverType
	if target, _, ok := types.ReferenceTarget(types.Underlying(receiverType)); ok {
		selfType = target
	}
	fnType := method.CallableTypeFor(selfType)
	if len(fnType.Params) == 0 {
		return fnType
	}
	if _, _, referenceReceiver := types.ReferenceTarget(types.Underlying(fnType.Params[0])); !referenceReceiver {
		if _, _, borrowedCarrier := types.ReferenceTarget(types.Underlying(receiverType)); !borrowedCarrier {
			fnType.Params[0] = receiverType
		}
	}
	return fnType
}

func (c *checker) mutableAddressableExpr(scope *symbols.Scope, expr ast.Expr) (bool, types.Type, *symbols.Symbol) {
	if c == nil {
		return false, nil, nil
	}
	return place.MutableAddressable(scope, expr, c.exprType, c.expandedDefaultBinding)
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
func (c *checker) qualifiedScopeType(scope *symbols.Scope, node *ast.ScopeResolution) types.Type {
	if c == nil || node == nil {
		return &types.InvalidType{}
	}
	var sym *symbols.Symbol
	if c.module != nil && c.module.SymbolIndex != nil {
		sym = c.module.SymbolIndex.Symbol(node)
	}
	if sym == nil {
		qualifier, member, imported := node.ImportValueMember()
		if !imported {
			return &types.InvalidType{}
		}
		resolved, ok := c.ctx.TypeResolver.LookupImportedSymbol(c.module, qualifier.Name, member.Name)
		if !ok || resolved.Symbol == nil {
			return &types.InvalidType{}
		}
		sym = resolved.Symbol
	}
	if c.module != nil && c.module.SymbolIndex != nil {
		c.module.SymbolIndex.Bind(node, sym)
	}
	if sym.Kind == symbols.SymbolVariant {
		return c.typeVariantConstruction(scope, node, node, nil)
	}
	t, ok := symbols.GetSymbolType(sym)
	if !ok || t == nil {
		return &types.UnknownType{}
	}
	return t
}
