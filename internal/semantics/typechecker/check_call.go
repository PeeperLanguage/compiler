package typechecker

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

func (c *checker) typeFreeExpr(scope *table.Scope, node *ast.FreeExpr) typeinfo.Type {
	if node == nil || node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	operandType := c.typeExpr(scope, node.Expr, nil)
	if typeinfo.IsInvalidOrUnknown(operandType) {
		return &typeinfo.InvalidType{}
	}
	if _, ok := typeinfo.Underlying(operandType).(*typeinfo.OwnedPtrType); !ok {
		c.ctx.Diagnostics.Add(invalidTypeError(node.Expr,
			fmt.Sprintf("free requires an owned pointer, got %s", typeinfo.TypeText(operandType))))
		return &typeinfo.InvalidType{}
	}
	return nil
}

func (c *checker) typePrintExpr(scope *table.Scope, node *ast.PrintExpr) typeinfo.Type {
	if node == nil || node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	operandType := c.typeExpr(scope, node.Expr, nil)
	if typeinfo.IsInvalidOrUnknown(operandType) {
		return &typeinfo.InvalidType{}
	}
	switch operand := typeinfo.Underlying(operandType).(type) {
	case *typeinfo.IntegerType:
		if operand.Bits > 64 {
			c.ctx.Diagnostics.Add(invalidTypeError(node.Expr,
				fmt.Sprintf("print supports integers up to 64 bits, got %s", typeinfo.TypeText(operandType))))
			return &typeinfo.InvalidType{}
		}
		return nil
	case *typeinfo.FloatType, *typeinfo.BoolType, *typeinfo.ByteType, *typeinfo.CStrType, *typeinfo.RawPtrType:
		return nil
	default:
		c.ctx.Diagnostics.Add(invalidTypeError(node.Expr,
			fmt.Sprintf("print requires a primitive scalar, got %s", typeinfo.TypeText(operandType))))
		return &typeinfo.InvalidType{}
	}
}

func (c *checker) typeCallExpr(scope *table.Scope, node *ast.CallExpr, expected typeinfo.Type) typeinfo.Type {
	if selector, ok := node.Callee.(*ast.SelectorExpr); ok && selector != nil {
		return c.typeSelectorCall(scope, selector, node)
	}
	if ident, ok := node.Callee.(*ast.Ident); ok && ident != nil {
		if sym := c.module.Semantics.ResolvedSymbols[ident.ID()]; sym != nil && sym.CompilerOp != "" {
			if sym.CompilerOp == symbols.CompilerOpAlloc {
				return c.typeAllocCall(scope, node)
			}
			return c.typeDynamicArrayOwnerCall(scope, node, sym.CompilerOp)
		}
	}
	calleeType := c.typeExpr(scope, node.Callee, expected)
	if sym := c.callableSymbol(node.Callee); sym != nil {
		c.expandCallDefaults(node, sym, c.callableModule(node.Callee))
	}
	argTypes := make([]typeinfo.Type, 0, len(node.Args))
	fnType, _ := calleeType.(*typeinfo.FuncType)
	for i, arg := range node.Args {
		var paramExpected typeinfo.Type
		if fnType != nil && i < len(fnType.Params) {
			paramExpected = fnType.Params[i]
		}
		argTypes = append(argTypes, c.typeExpr(scope, arg, paramExpected))
	}
	c.checkFunctionCall(node, calleeType, argTypes)
	return c.callReturnType(node, calleeType)
}

func (c *checker) typeDynamicArrayOwnerCall(scope *table.Scope, node *ast.CallExpr, op symbols.CompilerOp) typeinfo.Type {
	wantArgs := 2
	if op == symbols.CompilerOpResize {
		wantArgs = 3
	}
	if len(node.Args) != wantArgs {
		for _, arg := range node.Args {
			c.typeExpr(scope, arg, nil)
		}
		c.ctx.Diagnostics.Add(wrongArgumentCountError(node, len(node.Args), wantArgs))
		return &typeinfo.InvalidType{}
	}

	ownerType := c.typeExpr(scope, node.Args[0], nil)
	array, ok := typeinfo.Underlying(ownerType).(*typeinfo.ArrayType)
	if !ok || array == nil || !array.Dynamic || array.Elem == nil {
		for _, arg := range node.Args[1:] {
			c.typeExpr(scope, arg, nil)
		}
		c.ctx.Diagnostics.Add(invalidTypeError(node.Args[0],
			fmt.Sprintf("`%s` requires a dynamic-array owner `[]T` as first argument", op)))
		return &typeinfo.InvalidType{}
	}

	sizeType, ok := typeinfo.NumericTypeFromName("usize", c.ctx.Target)
	if !ok {
		panic("missing builtin usize type")
	}
	params := []typeinfo.Type{ownerType}
	switch op {
	case symbols.CompilerOpAppend:
		params = append(params, array.Elem)
	case symbols.CompilerOpReserve, symbols.CompilerOpShrink:
		params = append(params, sizeType)
	case symbols.CompilerOpResize:
		params = append(params, sizeType, array.Elem)
	default:
		panic(fmt.Sprintf("unsupported dynamic-array compiler operation %q", op))
	}

	fnType := &typeinfo.FuncType{Params: params, Return: ownerType}
	c.module.Semantics.ExprTypes[node.Callee.ID()] = fnType
	argTypes := make([]typeinfo.Type, 0, len(node.Args))
	argTypes = append(argTypes, ownerType)
	for i, arg := range node.Args[1:] {
		argTypes = append(argTypes, c.typeExpr(scope, arg, params[i+1]))
	}
	c.checkFunctionCall(node, fnType, argTypes)
	if op == symbols.CompilerOpResize && !typeinfo.IsImplicitCopyType(array.Elem) {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"resize requires implicitly copyable elements; grow Category B arrays with append",
			ast.LocOf(node), "")
	}
	return ownerType
}

func (c *checker) typeAllocCall(scope *table.Scope, node *ast.CallExpr) typeinfo.Type {
	wantArgs := 2
	if len(node.Args) < 1 || len(node.Args) > wantArgs {
		for _, arg := range node.Args {
			c.typeExpr(scope, arg, nil)
		}
		c.ctx.Diagnostics.Add(wrongArgumentCountError(node, len(node.Args), wantArgs))
		return &typeinfo.InvalidType{}
	}

	valueType := c.typeExpr(scope, node.Args[0], nil)
	if valueType == nil {
		return &typeinfo.InvalidType{}
	}

	if rejected := typeinfo.ContainsStoredReference(valueType); rejected {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Args[0],
			"alloc cannot store value containing a reference in owned heap storage"))
	}

	if !typeinfo.IsLowerableType(valueType) {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Args[0],
			"alloc target type is not lowerable in current compiler stage"))
	}

	allocType := &typeinfo.AllocatorType{}
	if len(node.Args) > 1 {
		allocatorValueType := c.typeExpr(scope, node.Args[1], allocType)
		if allocatorValueType != nil && !c.assignable(allocType, allocatorValueType) {
			d := typeMismatchError(node.Args[1],
				fmt.Sprintf("cannot implicitly convert %s to %s",
					typeinfo.TypeText(allocatorValueType), typeinfo.TypeText(allocType)))
			c.addInterfaceHint(d, allocType, allocatorValueType)
			c.ctx.Diagnostics.Add(d)
		}
	}

	return &typeinfo.OwnedPtrType{Target: valueType}
}

func (c *checker) typeSelectorCall(scope *table.Scope, selector *ast.SelectorExpr, call *ast.CallExpr) typeinfo.Type {
	baseType := c.typeExpr(scope, selector.Expr, nil)
	if baseType == nil || typeinfo.IsInvalidOrUnknown(baseType) {
		return &typeinfo.InvalidType{}
	}
	methodType, methodSym, ok := c.lookupMethodType(baseType, selector.Name.Name)
	if ok {
		if methodSym != nil {
			c.expandCallDefaults(call, methodSym, c.module)
		}
		if c.module != nil && c.module.Semantics != nil {
			c.module.Semantics.ExprTypes[selector.ID()] = methodType
		}
		argTypes := make([]typeinfo.Type, 0, len(call.Args)+1)
		argTypes = append(argTypes, baseType)
		fnType, _ := methodType.(*typeinfo.FuncType)
		for i, arg := range call.Args {
			var paramExpected typeinfo.Type
			if fnType != nil && i+1 < len(fnType.Params) {
				paramExpected = fnType.Params[i+1]
			}
			argTypes = append(argTypes, c.typeExpr(scope, arg, paramExpected))
		}
		c.checkMethodCall(scope, selector.Expr, call, methodType, argTypes)
		return c.callReturnType(call, methodType)
	}
	if field, _, fieldOK := typeinfo.LookupStructField(baseType, selector.Name.Name); fieldOK {
		c.ctx.Diagnostics.AddError(diagnostics.ErrNotCallable,
			fmt.Sprintf("field `%s` is not callable", selector.Name.Name), ast.LocOf(selector.Name), "").
			WithHelp(fmt.Sprintf("field `%s` has type %s — access it without `()`", selector.Name.Name, typeinfo.TypeText(field.Type)))
		return &typeinfo.InvalidType{}
	}
	methods := c.availableMethods(baseType)
	d := diagnostics.NewError(fmt.Sprintf("unknown method `%s`", selector.Name.Name)).
		WithCode(diagnostics.ErrMethodNotFound).
		WithPrimaryLabel(ast.LocOf(selector.Name), "")
	if len(methods) > 0 {
		d.WithHelp("available methods: " + strings.Join(methods, ", "))
	} else if match, ok := diagnostics.NearestName(selector.Name.Name, availableFields(baseType)); ok {
		d.WithHelp("did you mean field `" + match + "`?")
	}
	c.ctx.Diagnostics.Add(d)
	return &typeinfo.InvalidType{}
}

func (c *checker) checkFunctionCall(callExpr *ast.CallExpr, calleeType typeinfo.Type, args []typeinfo.Type) {
	if c == nil || callExpr == nil || calleeType == nil {
		return
	}
	fnType, ok := calleeType.(*typeinfo.FuncType)
	if !ok || fnType == nil {
		if !typeinfo.IsInvalidOrUnknown(calleeType) {
			c.ctx.Diagnostics.Add(notCallableError(callExpr, "call target is not a function"))
		}
		return
	}
	if len(args) != len(fnType.Params) {
		d := wrongArgumentCountError(callExpr, len(args), len(fnType.Params))
		paramDescs := make([]string, len(fnType.Params))
		for i, p := range fnType.Params {
			paramDescs[i] = typeinfo.TypeText(p)
		}
		d.WithHelp(fmt.Sprintf("expected parameters: (%s)", strings.Join(paramDescs, ", ")))
		c.ctx.Diagnostics.Add(d)
		return
	}
	for i, argType := range args {
		if argType == nil {
			c.ctx.Diagnostics.Add(invalidExpressionError(callExpr.Args[i],
				"argument requires a value-producing expression"))
			continue
		}
		paramType := fnType.Params[i]
		if paramType == nil {
			continue
		}
		if !c.assignable(paramType, argType) {
			d := typeMismatchError(callExpr.Args[i],
				fmt.Sprintf("cannot implicitly convert %s to %s",
					typeinfo.TypeText(argType), typeinfo.TypeText(paramType)))
			c.addInterfaceHint(d, paramType, argType)
			c.ctx.Diagnostics.Add(d)
			continue
		}
	}
}

func (c *checker) checkMethodCall(scope *table.Scope, receiverExpr ast.Expr, callExpr *ast.CallExpr, calleeType typeinfo.Type, args []typeinfo.Type) {
	if c == nil || callExpr == nil || calleeType == nil {
		return
	}
	fnType, ok := calleeType.(*typeinfo.FuncType)
	if !ok || fnType == nil {
		if !typeinfo.IsInvalidOrUnknown(calleeType) {
			c.ctx.Diagnostics.Add(notCallableError(callExpr, "call target is not a method"))
		}
		return
	}
	if len(args) != len(fnType.Params) {
		c.ctx.Diagnostics.Add(wrongArgumentCountError(callExpr, len(args)-1, len(fnType.Params)-1))
		return
	}
	for i, argType := range args {
		if argType == nil {
			if i > 0 {
				c.ctx.Diagnostics.Add(invalidExpressionError(callExpr.Args[i-1],
					"argument requires a value-producing expression"))
			}
			continue
		}
		paramType := fnType.Params[i]
		if paramType == nil {
			continue
		}
		if i == 0 {
			if refTarget, mutable, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(paramType)); ok && c.matchesReceiverTarget(refTarget, argType) {
				addressable := place.Addressable(scope, receiverExpr, func(e ast.Expr) typeinfo.Type {
					return c.typeExpr(scope, e, nil)
				}, c.expandedDefaultBinding)
				if mutable {
					addressable, _ = c.mutableAddressableExpr(scope, receiverExpr)
				}
				if addressable {
					continue
				}
				if mutable {
					if site, msg, ok := c.mutableReceiverDiagnostic(scope, receiverExpr); ok {
						c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment, msg, ast.LocOf(site), "immutable binding defined here")
						continue
					}
				}
			}
		}
		if !c.assignable(paramType, argType) {
			site := ast.Node(callExpr)
			if i > 0 && i-1 < len(callExpr.Args) {
				site = callExpr.Args[i-1]
			}
			c.ctx.Diagnostics.Add(typeMismatchError(site,
				fmt.Sprintf("cannot implicitly convert %s to %s",
					typeinfo.TypeText(argType), typeinfo.TypeText(paramType))))
			continue
		}
	}
}

func (c *checker) callableSymbol(callee ast.Expr) *symbols.Symbol {
	if c == nil || c.module == nil || callee == nil {
		return nil
	}
	switch node := callee.(type) {
	case *ast.Ident:
		if c.module.Semantics != nil {
			return c.module.Semantics.ResolvedSymbols[node.ID()]
		}
	case *ast.ScopeResolution:
		if resolved, ok := project.LookupImportedSymbol(c.ctx, c.module, node.Module.Name, node.Name.Name); ok {
			return resolved.Symbol
		}
	}
	return nil
}

func (c *checker) callableModule(callee ast.Expr) *project.Module {
	if c == nil || c.module == nil {
		return nil
	}
	if node, ok := callee.(*ast.ScopeResolution); ok && node != nil {
		if resolved, ok := project.LookupImportedSymbol(c.ctx, c.module, node.Module.Name, node.Name.Name); ok && resolved.Module != nil {
			return resolved.Module
		}
	}
	return c.module
}

func (c *checker) expandCallDefaults(call *ast.CallExpr, sym *symbols.Symbol, declModule *project.Module) {
	if c == nil || c.module == nil || c.module.Semantics == nil || call == nil || sym == nil {
		return
	}
	fn, ok := sym.ASTNode.(*ast.FnDecl)
	if !ok || fn == nil {
		return
	}
	params := fn.ParamsWithReceiver()
	offset := 0
	var receiver ast.Expr
	if selector, ok := call.Callee.(*ast.SelectorExpr); ok && selector != nil {
		offset = 1
		receiver = selector.Expr
	}
	firstDefault := -1
	for i, param := range params {
		if param.Default != nil {
			firstDefault = i
			break
		}
	}
	provided := len(call.Args) + offset
	if firstDefault < 0 || provided < firstDefault || provided >= len(params) {
		return
	}
	substitutions := make(map[string]ast.Expr, len(params))
	slotExprs := make([]ast.Expr, len(params))
	if receiver != nil && len(slotExprs) > 0 {
		slotExprs[0] = receiver
	}
	for i, arg := range call.Args {
		slot := i + offset
		if slot >= len(slotExprs) {
			break
		}
		slotExprs[slot] = arg
	}
	for i := 0; i < provided; i++ {
		if params[i].Name == nil || slotExprs[i] == nil {
			continue
		}
		substitutions[params[i].Name.Name] = slotExprs[i]
	}
	for i := provided; i < len(params); i++ {
		if params[i].Default == nil {
			return
		}
		ast.Inspect(params[i].Default, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok || ident == nil {
				return true
			}
			if replacement := substitutions[ident.Name]; replacement != nil && containsEffectfulExpression(replacement) {
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
					"default value reuses an effectful argument; bind it before the call", ast.LocOf(ident), "")
			}
			return true
		})
		expanded, clonedIDs := ast.SubstituteExpr(params[i].Default, substitutions)
		if declModule != nil && declModule.Semantics != nil {
			for originalID, clonedID := range clonedIDs {
				if resolved := declModule.Semantics.ResolvedSymbols[originalID]; resolved != nil {
					c.module.Semantics.ResolvedSymbols[clonedID] = resolved
					c.module.Semantics.ExpandedDefaultBindings[clonedID] = struct{}{}
				}
				if typ := declModule.Semantics.ExprTypes[originalID]; typ != nil {
					c.module.Semantics.ExprTypes[clonedID] = typ
				}
			}
		}
		call.Args = append(call.Args, expanded)
		slotExprs[i] = expanded
		if params[i].Name != nil {
			substitutions[params[i].Name.Name] = expanded
		}
	}
}

func containsEffectfulExpression(expr ast.Expr) bool {
	if expr == nil {
		return false
	}
	effectful := false
	ast.Inspect(expr, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.CallExpr, *ast.FreeExpr, *ast.PrintExpr:
			effectful = true
			return false
		default:
			return true
		}
	})
	return effectful
}

func (c *checker) callReturnType(call *ast.CallExpr, calleeType typeinfo.Type) typeinfo.Type {
	if c == nil {
		return &typeinfo.InvalidType{}
	}
	if calleeType != nil {
		if fnType, ok := calleeType.(*typeinfo.FuncType); ok && fnType != nil {
			if fnType.Return == nil {
				return nil
			}
			if !typeinfo.IsUnknown(fnType.Return) {
				return fnType.Return
			}
			if call != nil {
				c.ctx.Diagnostics.Add(invalidTypeError(call, "call has unknown return type"))
			}
			return &typeinfo.InvalidType{}
		}
	}
	return &typeinfo.InvalidType{}
}
