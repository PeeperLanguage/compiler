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
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

func (c *checker) typeFreeExpr(scope *symbols.Scope, node *ast.FreeExpr) typeinfo.Type {
	if node == nil || node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	operandType := c.typePayloadExpr(scope, node.Expr, nil)
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

func (c *checker) typePrintExpr(scope *symbols.Scope, node *ast.PrintExpr) typeinfo.Type {
	if node == nil || node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	operandType := c.typePayloadExpr(scope, node.Expr, nil)
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
	case *typeinfo.FloatType, *typeinfo.BoolType, *typeinfo.ByteType, *typeinfo.CStrType, *typeinfo.StringType, *typeinfo.RawPtrType:
		return nil
	default:
		c.ctx.Diagnostics.Add(invalidTypeError(node.Expr,
			fmt.Sprintf("print requires a primitive scalar, got %s", typeinfo.TypeText(operandType))))
		return &typeinfo.InvalidType{}
	}
}

func (c *checker) typeCallExpr(scope *symbols.Scope, node *ast.CallExpr) typeinfo.Type {
	effectiveArgs := c.module.Typechecking.CallArgumentsOrSource(node)
	if c.flow == nil {
		effectiveArgs = append([]ast.Expr(nil), node.Args...)
		c.module.Typechecking.EffectiveCallArguments[node.ID()] = effectiveArgs
	}
	if path, ok := node.Callee.(*ast.ScopeResolution); ok && path != nil && c.module.Bindings != nil {
		if sym := c.module.Bindings.NodeSymbols[path.ID()]; sym != nil && sym.Kind == symbols.SymbolVariant {
			for _, arg := range node.Args {
				c.typeExpr(scope, arg, nil)
			}
			c.ctx.Diagnostics.Add(notCallableError(node.Callee,
				"enum variants are not callable; use braces for data variants"))
			return &typeinfo.InvalidType{}
		}
	}
	if selector, ok := node.Callee.(*ast.SelectorExpr); ok && selector != nil {
		return c.typeSelectorCall(scope, selector, node)
	}
	if ident, ok := node.Callee.(*ast.Ident); ok && ident != nil && c.module.Bindings != nil {
		if sym := c.module.Bindings.NodeSymbols[ident.ID()]; sym != nil && sym.CompilerOp != "" {
			definition, found := intrinsics.LookupFunction(sym.CompilerOp)
			if !found {
				panic(fmt.Sprintf("missing intrinsic definition for compiler operation %q", sym.CompilerOp))
			}
			c.module.Typechecking.CompilerCalls[node.ID()] = typecheckresult.CompilerCall{Operation: definition.Operation, Kind: definition.Kind}
			switch definition.Kind {
			case intrinsics.FunctionAlloc:
				return c.typeAllocCall(scope, node)
			case intrinsics.FunctionCollection:
				return c.typeCollectionCall(scope, node, definition)
			case intrinsics.FunctionDynamicArrayOwner:
				return c.typeDynamicArrayOwnerCall(scope, node, definition)
			case intrinsics.FunctionFromBytes:
				return c.typeFromBytesCall(scope, node, definition)
			default:
				panic(fmt.Sprintf("unsupported intrinsic function kind %d for %q", definition.Kind, definition.Operation))
			}
		}
	}
	calleeType := c.typePayloadExpr(scope, node.Callee, nil)
	if sym := c.callableSymbol(node.Callee); sym != nil && c.flow == nil {
		effectiveArgs = c.expandCallDefaults(node, effectiveArgs, sym, c.callableModule(node.Callee))
		c.module.Typechecking.EffectiveCallArguments[node.ID()] = effectiveArgs
	}
	argTypes := make([]typeinfo.Type, 0, len(effectiveArgs))
	fnType, _ := calleeType.(*typeinfo.FuncType)
	for i, arg := range effectiveArgs {
		var paramExpected typeinfo.Type
		if fnType != nil && i < len(fnType.Params) {
			paramExpected = fnType.Params[i]
		}
		argTypes = append(argTypes, c.typeExpr(scope, arg, paramExpected))
	}
	c.checkCall(scope, nil, node, calleeType, effectiveArgs, argTypes)
	return c.callReturnType(node, calleeType)
}

func (c *checker) typeFromBytesCall(scope *symbols.Scope, node *ast.CallExpr, definition intrinsics.FunctionDefinition) typeinfo.Type {
	if !c.checkOptionalAllocatorArity(scope, node) {
		return &typeinfo.InvalidType{}
	}

	fnType := definition.Signature(nil, c.ctx.Target)
	if fnType == nil {
		panic("missing from_bytes signature")
	}
	c.module.Typechecking.ExprTypes[node.Callee.ID()] = fnType
	for i, arg := range node.Args {
		if i < len(fnType.Params) {
			c.publishValueUse(arg, fnType.Params[i])
		}
	}
	bytesType := c.typeExpr(scope, node.Args[0], fnType.Params[0])
	if !typeinfo.IsInvalidOrUnknown(bytesType) && !typeinfo.SameType(bytesType, fnType.Params[0]) {
		c.ctx.Diagnostics.Add(invalidTypeError(node.Args[0],
			"`from_bytes` requires a shared byte-slice view `&[..]byte`"))
	}
	if len(node.Args) == 2 {
		allocatorType := c.typeExpr(scope, node.Args[1], fnType.Params[1])
		if !typeinfo.IsInvalidOrUnknown(allocatorType) && !c.assignable(fnType.Params[1], allocatorType, node.Args[1]) {
			c.ctx.Diagnostics.Add(typeMismatchError(node.Args[1],
				fmt.Sprintf("cannot implicitly convert %s to Allocator", typeinfo.TypeText(allocatorType))))
		}
	}
	return fnType.Return
}

func (c *checker) typeCollectionCall(scope *symbols.Scope, node *ast.CallExpr, definition intrinsics.FunctionDefinition) typeinfo.Type {
	if len(node.Args) != 1 {
		for _, arg := range node.Args {
			c.typeExpr(scope, arg, nil)
		}
		displayArgs := len(node.Args)
		displayWant := 1
		if node.Piped {
			displayArgs--
			displayWant--
		}
		c.ctx.Diagnostics.Add(wrongArgumentCountError(node, displayArgs, displayWant))
		return &typeinfo.InvalidType{}
	}

	baseType := c.typeExpr(scope, node.Args[0], nil)
	fnType := definition.Signature(baseType, c.ctx.Target)
	if fnType == nil {
		c.ctx.Diagnostics.Add(invalidTypeError(node.Args[0],
			fmt.Sprintf("`%s` does not support %s", definition.Operation, typeinfo.TypeText(baseType))))
		return &typeinfo.InvalidType{}
	}
	c.module.Typechecking.ExprTypes[node.Callee.ID()] = fnType
	c.checkCall(scope, nil, node, fnType, node.Args, []typeinfo.Type{baseType})
	return c.callReturnType(node, fnType)
}

func (c *checker) typeDynamicArrayOwnerCall(scope *symbols.Scope, node *ast.CallExpr, definition intrinsics.FunctionDefinition) typeinfo.Type {
	op := definition.Operation
	genericSignature := definition.Signature(nil, c.ctx.Target)
	if genericSignature == nil {
		panic(fmt.Sprintf("missing generic dynamic-array signature for %q", op))
	}
	wantArgs := len(genericSignature.Params)
	if len(node.Args) != wantArgs {
		for _, arg := range node.Args {
			c.typeExpr(scope, arg, nil)
		}
		displayArgs, displayWant := len(node.Args), wantArgs
		if node.Piped {
			displayArgs--
			displayWant--
		}
		c.ctx.Diagnostics.Add(wrongArgumentCountError(node, displayArgs, displayWant))
		return &typeinfo.InvalidType{}
	}

	firstArgType := c.typeExpr(scope, node.Args[0], nil)
	ownerType := firstArgType
	if targetType, _, referenced := typeinfo.ReferenceTarget(typeinfo.Underlying(firstArgType)); referenced {
		ownerType = targetType
	}
	array, ok := typeinfo.Underlying(ownerType).(*typeinfo.ArrayType)
	if !ok || array == nil || array.Shape != typeinfo.ArrayOwner || array.Elem == nil {
		for _, arg := range node.Args[1:] {
			c.typeExpr(scope, arg, nil)
		}
		c.ctx.Diagnostics.Add(invalidTypeError(node.Args[0],
			fmt.Sprintf("`%s` requires a dynamic-array owner `[]T` as first argument", op)))
		return &typeinfo.InvalidType{}
	}

	fnType := definition.Signature(ownerType, c.ctx.Target)
	if fnType == nil {
		panic(fmt.Sprintf("missing dynamic-array signature for %q", op))
	}

	c.module.Typechecking.ExprTypes[node.Callee.ID()] = fnType
	argTypes := make([]typeinfo.Type, 0, len(node.Args))
	argTypes = append(argTypes, firstArgType)
	for i, arg := range node.Args[1:] {
		argTypes = append(argTypes, c.typeExpr(scope, arg, fnType.Params[i+1]))
	}
	c.checkCall(scope, nil, node, fnType, node.Args, argTypes)
	if op == symbols.CompilerOpResize && !typeinfo.IsImplicitCopyType(array.Elem) {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"resize requires implicitly copyable elements; grow Category B arrays with append",
			ast.LocOf(node), "")
	}
	return nil
}

func (c *checker) typeAllocCall(scope *symbols.Scope, node *ast.CallExpr) typeinfo.Type {
	if !c.checkOptionalAllocatorArity(scope, node) {
		return &typeinfo.InvalidType{}
	}

	// The intrinsic has no parameter types to classify against, so the use kinds
	// are published from its own semantics, before any type-dependent exit: the
	// analyzer consumes them on diagnostics-continued paths too.
	if c.module != nil && c.module.Typechecking != nil {
		c.module.Typechecking.ValueUses[node.Args[0].ID()] = typeinfo.UseMove
		if len(node.Args) > 1 {
			c.module.Typechecking.ValueUses[node.Args[1].ID()] = typeinfo.UseRead
		}
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
		if allocatorValueType != nil && !c.assignable(allocType, allocatorValueType, node.Args[1]) {
			d := typeMismatchError(node.Args[1],
				fmt.Sprintf("cannot implicitly convert %s to %s",
					typeinfo.TypeText(allocatorValueType), typeinfo.TypeText(allocType)))
			c.addInterfaceHint(d, allocType, allocatorValueType)
			c.ctx.Diagnostics.Add(d)
		}
	}

	return &typeinfo.OwnedPtrType{Target: valueType}
}

// publishValueUse records the ownership use kind a parameter position imposes
// on its argument, keyed by the argument expression the ownership analyzer
// walks. Reference parameters publish UseRead: the kind is recorded for
// completeness, while the borrow machinery in ownership still governs them.
func (c *checker) publishValueUse(arg ast.Expr, paramType typeinfo.Type) {
	if c == nil || arg == nil || paramType == nil || c.module == nil || c.module.Typechecking == nil {
		return
	}
	use := typeinfo.UseMove
	if _, _, reference := typeinfo.ReferenceValueTarget(paramType); reference || typeinfo.IsImplicitCopyType(paramType) {
		use = typeinfo.UseRead
	}
	c.module.Typechecking.ValueUses[arg.ID()] = use
}

func (c *checker) checkOptionalAllocatorArity(scope *symbols.Scope, node *ast.CallExpr) bool {
	const minArgs, maxArgs = 1, 2
	argCount := len(node.Args)
	if argCount >= minArgs && argCount <= maxArgs {
		return true
	}
	for _, arg := range node.Args {
		c.typeExpr(scope, arg, nil)
	}
	wantArgs := maxArgs
	if argCount < minArgs {
		wantArgs = minArgs
	}
	if node.Piped {
		argCount--
		wantArgs--
	}
	c.ctx.Diagnostics.Add(wrongArgumentCountError(node, argCount, wantArgs))
	return false
}

func (c *checker) typeSelectorCall(scope *symbols.Scope, selector *ast.SelectorExpr, call *ast.CallExpr) typeinfo.Type {
	baseType := c.typePayloadExpr(scope, selector.Expr, nil)
	if baseType == nil || typeinfo.IsInvalidOrUnknown(baseType) {
		return &typeinfo.InvalidType{}
	}
	method, ok := c.lookupCallableMember(baseType, selector.Name.Name)
	if ok {
		methodType, methodSym := method.Type, method.Symbol
		effectiveArgs := c.module.Typechecking.EffectiveCallArguments[call.ID()]
		if methodSym != nil && methodSym.CompilerOp == "" && c.flow == nil {
			effectiveArgs = c.expandCallDefaults(call, effectiveArgs, methodSym, c.module)
			c.module.Typechecking.EffectiveCallArguments[call.ID()] = effectiveArgs
		}
		if c.module != nil {
			if c.module.Typechecking != nil {
				c.module.Typechecking.ExprTypes[selector.ID()] = methodType
			}
			if methodSym != nil && c.module.Bindings != nil {
				c.module.Bindings.NodeSymbols[selector.Name.ID()] = methodSym
			}
		}
		argTypes := make([]typeinfo.Type, 0, len(effectiveArgs)+1)
		argTypes = append(argTypes, baseType)
		fnType, _ := methodType.(*typeinfo.FuncType)
		for i, arg := range effectiveArgs {
			var paramExpected typeinfo.Type
			if fnType != nil && i+1 < len(fnType.Params) {
				paramExpected = fnType.Params[i+1]
			}
			argTypes = append(argTypes, c.typeExpr(scope, arg, paramExpected))
		}
		c.checkCall(scope, selector.Expr, call, methodType, effectiveArgs, argTypes)
		return c.callReturnType(call, methodType)
	}
	if field, _, fieldOK := typeinfo.LookupStructField(baseType, selector.Name.Name); fieldOK {
		c.ctx.Diagnostics.AddError(diagnostics.ErrNotCallable,
			fmt.Sprintf("field `%s` is not callable", selector.Name.Name), ast.LocOf(selector.Name), "").
			WithHelp(fmt.Sprintf("field `%s` has type %s - access it without `()`", selector.Name.Name, typeinfo.TypeText(field.Type)))
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

func (c *checker) checkCall(scope *symbols.Scope, receiverExpr ast.Expr, callExpr *ast.CallExpr, calleeType typeinfo.Type, argExprs []ast.Expr, args []typeinfo.Type) {
	if c == nil || callExpr == nil || calleeType == nil {
		return
	}
	fnType, ok := calleeType.(*typeinfo.FuncType)
	if !ok || fnType == nil {
		if !typeinfo.IsInvalidOrUnknown(calleeType) {
			kind := "function"
			if receiverExpr != nil {
				kind = "method"
			}
			c.ctx.Diagnostics.Add(notCallableError(callExpr, "call target is not a "+kind))
		}
		return
	}
	callArgOffset := 0
	if receiverExpr != nil {
		callArgOffset = 1
	}
	displayOffset := callArgOffset
	if callExpr.Piped {
		displayOffset = 1
	}
	if len(args) != len(fnType.Params) {
		d := wrongArgumentCountError(callExpr, len(args)-displayOffset, len(fnType.Params)-displayOffset)
		if receiverExpr == nil {
			paramDescs := make([]string, len(fnType.Params)-displayOffset)
			for i, p := range fnType.Params[displayOffset:] {
				paramDescs[i] = typeinfo.TypeText(p)
			}
			d.WithHelp(fmt.Sprintf("expected parameters: (%s)", strings.Join(paramDescs, ", ")))
		}
		c.ctx.Diagnostics.Add(d)
		return
	}
	for i, argType := range args {
		argIndex := i - callArgOffset
		var argExpr ast.Expr
		if argIndex >= 0 && argIndex < len(argExprs) {
			argExpr = argExprs[argIndex]
		}
		var implicitExpr ast.Expr
		if i == 0 {
			if receiverExpr != nil {
				implicitExpr = receiverExpr
			} else if callExpr.Piped && len(argExprs) > 0 {
				implicitExpr = argExprs[0]
			}
		}
		if argType == nil {
			if i >= callArgOffset {
				site := ast.Node(callExpr)
				if argExpr != nil {
					site = argExpr
				}
				c.ctx.Diagnostics.Add(invalidExpressionError(site,
					"argument requires a value-producing expression"))
			}
			continue
		}
		paramType := fnType.Params[i]
		if paramType == nil {
			continue
		}
		c.publishValueUse(implicitExpr, paramType)
		if argIndex >= 0 {
			c.publishValueUse(argExpr, paramType)
		}
		if implicitExpr != nil && c.acceptImplicitCallArgument(scope, implicitExpr, argType, paramType) {
			c.module.Typechecking.ImplicitCallArguments[implicitExpr.ID()] = paramType
			continue
		}
		site := ast.Node(callExpr)
		var conversion ast.Expr
		if implicitExpr != nil {
			conversion = implicitExpr
		}
		if argExpr != nil {
			conversion = argExpr
			site = argExpr
		}
		if !c.assignable(paramType, argType, conversion) {
			d := typeMismatchError(site,
				fmt.Sprintf("cannot implicitly convert %s to %s",
					typeinfo.TypeText(argType), typeinfo.TypeText(paramType)))
			c.addInterfaceHint(d, paramType, argType)
			c.ctx.Diagnostics.Add(d)
			continue
		}
	}
}

// acceptImplicitCallArgument is the single semantic gate for method receivers
// and piped argument zero. Ordinary call arguments remain explicit.
func (c *checker) acceptImplicitCallArgument(scope *symbols.Scope, expr ast.Expr, argType, paramType typeinfo.Type) bool {
	refTarget, mutable, reference := typeinfo.ReferenceTarget(typeinfo.Underlying(paramType))
	if !reference || !c.matchesImplicitCallTarget(refTarget, argType) {
		return false
	}
	addressable := place.Addressable(scope, expr, func(e ast.Expr) typeinfo.Type {
		return c.typeExpr(scope, e, nil)
	}, c.module.ExpandedDefaultBinding)
	var mutableBinding *symbols.Symbol
	if mutable {
		addressable, _, mutableBinding = c.mutableAddressableExpr(scope, expr)
	}
	if addressable {
		if mutableBinding != nil {
			mutableBinding.RequiresMutable = true
		}
		return true
	}
	if mutable {
		if site, msg, ok := c.mutableImplicitArgumentDiagnostic(scope, expr); ok {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment, msg, ast.LocOf(site), "immutable binding defined here")
			return true
		}
		return false
	}
	// Shared implicit arguments may borrow a temporary for this full expression.
	return true
}

func (c *checker) matchesImplicitCallTarget(target, arg typeinfo.Type) bool {
	if c.matchesReceiverTarget(target, arg) {
		return true
	}
	slice, sliceTarget := typeinfo.Underlying(target).(*typeinfo.ArrayType)
	array, arrayArg := typeinfo.Underlying(arg).(*typeinfo.ArrayType)
	return sliceTarget && arrayArg && slice != nil && array != nil &&
		slice.Shape == typeinfo.ArraySlice && array.Shape != typeinfo.ArraySlice &&
		typeinfo.SameType(slice.Elem, array.Elem)
}

func (c *checker) callableSymbol(callee ast.Expr) *symbols.Symbol {
	if c == nil || c.module == nil || callee == nil {
		return nil
	}
	switch node := callee.(type) {
	case *ast.Ident:
		if c.module.Bindings != nil {
			return c.module.Bindings.NodeSymbols[node.ID()]
		}
	case *ast.ScopeResolution:
		qualifier, member, imported := node.ImportValueMember()
		if imported {
			if resolved, ok := project.LookupImportedSymbol(c.ctx, c.module, qualifier.Name, member.Name); ok {
				return resolved.Symbol
			}
		}
	}
	return nil
}

func (c *checker) callableModule(callee ast.Expr) *project.Module {
	if c == nil || c.module == nil {
		return nil
	}
	if node, ok := callee.(*ast.ScopeResolution); ok && node != nil {
		if qualifier, member, imported := node.ImportValueMember(); imported {
			if resolved, ok := project.LookupImportedSymbol(c.ctx, c.module, qualifier.Name, member.Name); ok && resolved.Module != nil {
				return resolved.Module
			}
		}
	}
	return c.module
}

func (c *checker) expandCallDefaults(call *ast.CallExpr, args []ast.Expr, sym *symbols.Symbol, declModule *project.Module) []ast.Expr {
	effectiveArgs := append([]ast.Expr(nil), args...)
	if c == nil || c.module == nil || c.module.Bindings == nil || c.module.Typechecking == nil || call == nil || sym == nil {
		return effectiveArgs
	}
	fn, ok := sym.ASTNode.(*ast.FnDecl)
	if !ok || fn == nil {
		return effectiveArgs
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
	provided := len(effectiveArgs) + offset
	if firstDefault < 0 || provided < firstDefault || provided >= len(params) {
		return effectiveArgs
	}
	for i := provided; i < len(params); i++ {
		if params[i].Default == nil {
			return effectiveArgs
		}
	}
	substitutions := make(map[string]ast.Expr, len(params))
	slotExprs := make([]ast.Expr, len(params))
	if receiver != nil && len(slotExprs) > 0 {
		slotExprs[0] = receiver
	}
	for i, arg := range effectiveArgs {
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
		expanded, defaultClones, argumentClones := ast.SubstituteExpr(params[i].Default, substitutions)
		if declModule != nil && declModule.Bindings != nil {
			for clonedID, originalID := range defaultClones {
				if resolved := declModule.Bindings.NodeSymbols[originalID]; resolved != nil {
					c.module.Bindings.NodeSymbols[clonedID] = resolved
					c.module.Typechecking.ExpandedDefaultBindings[clonedID] = struct{}{}
				}
				copyExpressionEvidence(c.module, declModule, clonedID, originalID)
			}
		}
		for clonedID, originalID := range argumentClones {
			if resolved := c.module.Bindings.NodeSymbols[originalID]; resolved != nil {
				c.module.Bindings.NodeSymbols[clonedID] = resolved
			}
			if _, ok := c.module.Typechecking.ExpandedDefaultBindings[originalID]; ok {
				c.module.Typechecking.ExpandedDefaultBindings[clonedID] = struct{}{}
			}
			copyExpressionEvidence(c.module, c.module, clonedID, originalID)
		}
		effectiveArgs = append(effectiveArgs, expanded)
		slotExprs[i] = expanded
		if params[i].Name != nil {
			substitutions[params[i].Name.Name] = expanded
		}
	}
	return effectiveArgs
}

func copyExpressionEvidence(dst, src *project.Module, dstID, srcID ast.NodeID) {
	if dst == nil || dst.Typechecking == nil || src == nil {
		return
	}
	if typ := src.BaseExprType(srcID); typ != nil {
		dst.Typechecking.ExprTypes[dstID] = typ
	}
	if src.Typechecking == nil {
		return
	}
	if implementations := src.Typechecking.InterfaceImplementations[srcID]; implementations != nil {
		dst.Typechecking.InterfaceImplementations[dstID] = implementations
	}
	if conversion, ok := src.Typechecking.ImplicitConversions[srcID]; ok {
		dst.Typechecking.ImplicitConversions[dstID] = conversion
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
