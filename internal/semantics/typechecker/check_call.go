package typechecker

import (
	"compiler/internal/source"
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/types"
)

func (c *checker) typeFreeExpr(scope *symbols.Scope, node *ast.FreeExpr) types.Type {
	if node == nil || node.Expr == nil {
		return &types.InvalidType{}
	}
	operandType := c.typePayloadExpr(scope, node.Expr, nil)
	if types.IsInvalidOrUnknown(operandType) {
		return &types.InvalidType{}
	}
	if _, ok := types.Underlying(operandType).(*types.OwnedPtrType); !ok {
		c.ctx.Diagnostics.Add(invalidTypeError(node.Expr,
			fmt.Sprintf("free requires an owned pointer, got %s", types.TypeText(operandType))))
		return &types.InvalidType{}
	}
	return nil
}

func (c *checker) typePrintExpr(scope *symbols.Scope, node *ast.PrintExpr) types.Type {
	if node == nil || node.Expr == nil {
		return &types.InvalidType{}
	}
	operandType := c.typePayloadExpr(scope, node.Expr, nil)
	if types.IsInvalidOrUnknown(operandType) {
		return &types.InvalidType{}
	}
	switch operand := types.Underlying(operandType).(type) {
	case *types.IntegerType:
		if operand.Bits > 64 {
			c.ctx.Diagnostics.Add(invalidTypeError(node.Expr,
				fmt.Sprintf("print supports integers up to 64 bits, got %s", types.TypeText(operandType))))
			return &types.InvalidType{}
		}
		return nil
	case *types.FloatType, *types.BoolType, *types.ByteType, *types.CStrType, *types.StringType, *types.RawPtrType:
		return nil
	default:
		c.ctx.Diagnostics.Add(invalidTypeError(node.Expr,
			fmt.Sprintf("print requires a primitive scalar, got %s", types.TypeText(operandType))))
		return &types.InvalidType{}
	}
}

func (c *checker) typeCallExpr(scope *symbols.Scope, node *ast.CallExpr) types.Type {
	if c.reusedCall == node {
		if typ := c.evidence.ExprType(node.ID()); typ != nil {
			return typ
		}
	}
	effectiveArgs := append([]ast.Expr(nil), node.Args...)
	c.evidence.RecordCallArguments(node.ID(), effectiveArgs)
	if path, ok := node.Callee.(*ast.ScopeResolution); ok && path != nil && c.module.SymbolIndex != nil {
		if sym := c.module.SymbolIndex.Symbol(path); sym != nil && sym.Kind == symbols.SymbolVariant {
			for _, arg := range node.Args {
				c.typeExpr(scope, arg, nil)
			}
			c.ctx.Diagnostics.Add(notCallableError(node.Callee,
				"enum variants are not callable; use braces for data variants"))
			return &types.InvalidType{}
		}
	}
	if selector, ok := node.Callee.(*ast.SelectorExpr); ok && selector != nil {
		return c.typeSelectorCall(scope, selector, node)
	}
	if ident, ok := node.Callee.(*ast.Ident); ok && ident != nil && c.module.SymbolIndex != nil {
		if sym := c.module.SymbolIndex.Symbol(ident); sym != nil && sym.CompilerOp != "" {
			definition, found := intrinsics.LookupFunction(sym.CompilerOp)
			if !found {
				panic(fmt.Sprintf("missing intrinsic definition for compiler operation %q", sym.CompilerOp))
			}
			c.evidence.RecordCompilerCall(node.ID(), thir.CompilerCall{Operation: definition.Operation, Kind: definition.Kind})
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
	if sym, declModule := c.defaultCallDeclaration(node.Callee); sym != nil {
		effectiveArgs = c.expandCallDefaults(node, effectiveArgs, sym, declModule)
		c.evidence.RecordCallArguments(node.ID(), effectiveArgs)
	}
	argTypes := make([]types.Type, 0, len(effectiveArgs))
	fnType, _ := calleeType.(*types.FuncType)
	for i, arg := range effectiveArgs {
		var paramExpected types.Type
		if fnType != nil && i < len(fnType.Params) {
			paramExpected = fnType.Params[i]
		}
		argTypes = append(argTypes, c.typeExpr(scope, arg, paramExpected))
	}
	c.checkCall(scope, nil, node, calleeType, effectiveArgs, argTypes)
	return c.callReturnType(node, calleeType)
}

func (c *checker) typeFromBytesCall(scope *symbols.Scope, node *ast.CallExpr, definition intrinsics.FunctionDefinition) types.Type {
	if !c.checkOptionalAllocatorArity(scope, node) {
		return &types.InvalidType{}
	}

	fnType := definition.Signature(nil, c.ctx.Target)
	if fnType == nil {
		panic("missing from_bytes signature")
	}
	c.evidence.RecordExprType(node.Callee.ID(), fnType)
	for i, arg := range node.Args {
		if i < len(fnType.Params) {
			c.publishValueUse(arg, fnType.Params[i])
		}
	}
	bytesType := c.typeExpr(scope, node.Args[0], fnType.Params[0])
	if !types.IsInvalidOrUnknown(bytesType) && !types.IsSameType(bytesType, fnType.Params[0]) {
		c.ctx.Diagnostics.Add(invalidTypeError(node.Args[0],
			"`from_bytes` requires a shared byte-slice view `&[..]byte`"))
	}
	if len(node.Args) == 2 {
		allocatorType := c.typeExpr(scope, node.Args[1], fnType.Params[1])
		if !types.IsInvalidOrUnknown(allocatorType) && !c.isAssignable(fnType.Params[1], allocatorType, node.Args[1]) {
			c.ctx.Diagnostics.Add(typeMismatchError(node.Args[1],
				fmt.Sprintf("cannot implicitly convert %s to Allocator", types.TypeText(allocatorType))))
		}
	}
	return fnType.Return
}

func (c *checker) typeCollectionCall(scope *symbols.Scope, node *ast.CallExpr, definition intrinsics.FunctionDefinition) types.Type {
	if len(node.Args) != 1 {
		for _, arg := range node.Args {
			c.typeExpr(scope, arg, nil)
		}
		displayArgs := len(node.Args)
		displayWant := 1
		if node.IsPiped {
			displayArgs--
			displayWant--
		}
		c.ctx.Diagnostics.Add(wrongArgumentCountError(node, displayArgs, displayWant))
		return &types.InvalidType{}
	}

	baseType := c.typeExpr(scope, node.Args[0], nil)
	fnType := definition.Signature(baseType, c.ctx.Target)
	if fnType == nil {
		c.ctx.Diagnostics.Add(invalidTypeError(node.Args[0],
			fmt.Sprintf("`%s` does not support %s", definition.Operation, types.TypeText(baseType))))
		return &types.InvalidType{}
	}
	c.evidence.RecordExprType(node.Callee.ID(), fnType)
	c.checkCall(scope, nil, node, fnType, node.Args, []types.Type{baseType})
	return c.callReturnType(node, fnType)
}

func (c *checker) typeDynamicArrayOwnerCall(scope *symbols.Scope, node *ast.CallExpr, definition intrinsics.FunctionDefinition) types.Type {
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
		if node.IsPiped {
			displayArgs--
			displayWant--
		}
		c.ctx.Diagnostics.Add(wrongArgumentCountError(node, displayArgs, displayWant))
		return &types.InvalidType{}
	}

	firstArgType := c.typeExpr(scope, node.Args[0], nil)
	ownerType := firstArgType
	if targetType, _, referenced := types.ReferenceTarget(types.Underlying(firstArgType)); referenced {
		ownerType = targetType
	}
	array, ok := types.Underlying(ownerType).(*types.ArrayType)
	if !ok || array == nil || array.Shape != types.ArrayOwner || array.Elem == nil {
		for _, arg := range node.Args[1:] {
			c.typeExpr(scope, arg, nil)
		}
		c.ctx.Diagnostics.Add(invalidTypeError(node.Args[0],
			fmt.Sprintf("`%s` requires a dynamic-array owner `[]T` as first argument", op)))
		return &types.InvalidType{}
	}

	fnType := definition.Signature(ownerType, c.ctx.Target)
	if fnType == nil {
		panic(fmt.Sprintf("missing dynamic-array signature for %q", op))
	}

	c.evidence.RecordExprType(node.Callee.ID(), fnType)
	argTypes := make([]types.Type, 0, len(node.Args))
	argTypes = append(argTypes, firstArgType)
	for i, arg := range node.Args[1:] {
		argTypes = append(argTypes, c.typeExpr(scope, arg, fnType.Params[i+1]))
	}
	c.checkCall(scope, nil, node, fnType, node.Args, argTypes)
	if op == symbols.CompilerOpResize && types.OwnershipCapabilityOf(array.Elem).Copy != types.CopyImplicit {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"resize requires implicitly copyable elements; grow Category B arrays with append",
			ast.LocOf(node), "")
	}
	return nil
}

func (c *checker) typeAllocCall(scope *symbols.Scope, node *ast.CallExpr) types.Type {
	if !c.checkOptionalAllocatorArity(scope, node) {
		return &types.InvalidType{}
	}

	// The intrinsic has no parameter types to classify against, so the use kinds
	// are published from its own semantics, before any type-dependent exit: the
	// analyzer consumes them on diagnostics-continued paths too.
	if c.module != nil && c.evidence != nil {
		c.evidence.RecordValueUse(node.Args[0].ID(), types.UseMove)
		if len(node.Args) > 1 {
			c.evidence.RecordValueUse(node.Args[1].ID(), types.UseRead)
		}
	}

	valueType := c.typeExpr(scope, node.Args[0], nil)
	if valueType == nil {
		return &types.InvalidType{}
	}

	if rejected := types.ContainsStoredReference(valueType); rejected {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Args[0],
			"alloc cannot store value containing a reference in owned heap storage"))
	}

	if !types.IsLowerableType(valueType) {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Args[0],
			"alloc target type is not lowerable in current compiler stage"))
	}

	allocType := &types.AllocatorType{}
	if len(node.Args) > 1 {
		allocatorValueType := c.typeExpr(scope, node.Args[1], allocType)
		if allocatorValueType != nil && !c.isAssignable(allocType, allocatorValueType, node.Args[1]) {
			d := typeMismatchError(node.Args[1],
				fmt.Sprintf("cannot implicitly convert %s to %s",
					types.TypeText(allocatorValueType), types.TypeText(allocType)))
			c.addInterfaceHint(d, allocType, allocatorValueType)
			c.ctx.Diagnostics.Add(d)
		}
	}

	return &types.OwnedPtrType{Target: valueType}
}

// publishValueUse records the ownership use kind a parameter position imposes
// on its argument, keyed by the argument expression the ownership analyzer
// walks. Reference parameters publish UseRead: the kind is recorded for
// completeness, while the borrow machinery in ownership still governs them.
func (c *checker) publishValueUse(arg ast.Expr, paramType types.Type) {
	if c == nil || arg == nil || paramType == nil || c.module == nil || c.evidence == nil {
		return
	}
	use := types.UseMove
	_, isMutable, isReference := types.ReferenceValueTarget(paramType)
	if isReference || types.OwnershipCapabilityOf(paramType).Copy == types.CopyImplicit {
		use = types.UseRead
	}
	c.evidence.RecordValueUse(arg.ID(), use)
	// A reference parameter borrows its argument. The use kind cannot carry
	// that: an implicit-copy argument publishes UseRead too, so a consumer could
	// not tell a borrow from a plain read.
	if isReference {
		c.evidence.RecordReferenceArgument(arg.ID(), isMutable)
	}
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
	if node.IsPiped {
		argCount--
		wantArgs--
	}
	c.ctx.Diagnostics.Add(wrongArgumentCountError(node, argCount, wantArgs))
	return false
}

func (c *checker) typeSelectorCall(scope *symbols.Scope, selector *ast.SelectorExpr, call *ast.CallExpr) types.Type {
	baseType := c.typePayloadExpr(scope, selector.Expr, nil)
	if baseType == nil || types.IsInvalidOrUnknown(baseType) || types.ContainsInvalid(baseType) {
		return &types.InvalidType{}
	}
	method, ok := c.lookupCallableMember(baseType, selector.Name.Name)
	if ok {
		methodType, methodSym := method.Type, method.Symbol
		effectiveArgs := append([]ast.Expr(nil), call.Args...)
		if methodSym != nil && methodSym.CompilerOp == "" {
			effectiveArgs = c.expandCallDefaults(call, effectiveArgs, methodSym, c.module)
			c.evidence.RecordCallArguments(call.ID(), effectiveArgs)
		}
		if c.module != nil {
			if c.evidence != nil {
				c.evidence.RecordExprType(selector.ID(), methodType)
			}
			if methodSym != nil && c.module.SymbolIndex != nil {
				c.module.SymbolIndex.Bind(selector.Name, methodSym)
			}
		}
		argTypes := make([]types.Type, 0, len(effectiveArgs)+1)
		argTypes = append(argTypes, baseType)
		fnType, _ := methodType.(*types.FuncType)
		for i, arg := range effectiveArgs {
			var paramExpected types.Type
			if fnType != nil && i+1 < len(fnType.Params) {
				paramExpected = fnType.Params[i+1]
			}
			argTypes = append(argTypes, c.typeExpr(scope, arg, paramExpected))
		}
		c.checkCall(scope, selector.Expr, call, methodType, effectiveArgs, argTypes)
		return c.callReturnType(call, methodType)
	}
	if field, _, fieldOK := types.LookupStructField(baseType, selector.Name.Name); fieldOK {
		c.ctx.Diagnostics.AddError(diagnostics.ErrNotCallable,
			fmt.Sprintf("field `%s` is not callable", selector.Name.Name), ast.LocOf(selector.Name), "").
			Help(fmt.Sprintf("field `%s` has type %s - access it without `()`", selector.Name.Name, types.TypeText(field.Type)))
		return &types.InvalidType{}
	}
	methods := c.availableMethods(baseType)
	d := diagnostics.NewError(fmt.Sprintf("unknown method `%s`", selector.Name.Name)).
		WithCode(diagnostics.ErrMethodNotFound).
		WithPrimaryLabel(ast.LocOf(selector.Name), "")
	if len(methods) > 0 {
		d.Help("available methods: " + strings.Join(methods, ", "))
	} else if match, ok := diagnostics.NearestName(selector.Name.Name, availableFields(baseType)); ok {
		d.Help("did you mean field `" + match + "`?")
	}
	c.ctx.Diagnostics.Add(d)
	return &types.InvalidType{}
}

func (c *checker) checkCall(scope *symbols.Scope, receiverExpr ast.Expr, callExpr *ast.CallExpr, calleeType types.Type, argExprs []ast.Expr, args []types.Type) {
	if c == nil || callExpr == nil || calleeType == nil {
		return
	}
	fnType, ok := calleeType.(*types.FuncType)
	if !ok || fnType == nil {
		if !types.IsInvalidOrUnknown(calleeType) {
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
	if callExpr.IsPiped {
		displayOffset = 1
	}
	if len(args) != len(fnType.Params) {
		d := wrongArgumentCountError(callExpr, len(args)-displayOffset, len(fnType.Params)-displayOffset)
		if receiverExpr == nil {
			paramDescs := make([]string, len(fnType.Params)-displayOffset)
			for i, p := range fnType.Params[displayOffset:] {
				paramDescs[i] = types.TypeText(p)
			}
			d.Help(fmt.Sprintf("expected parameters: (%s)", strings.Join(paramDescs, ", ")))
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
			} else if callExpr.IsPiped && len(argExprs) > 0 {
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
			c.evidence.RecordImplicitCallArgument(implicitExpr.ID(), paramType)
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
		if !c.isAssignable(paramType, argType, conversion) {
			d := typeMismatchError(site,
				fmt.Sprintf("cannot implicitly convert %s to %s",
					types.TypeText(argType), types.TypeText(paramType)))
			c.addInterfaceHint(d, paramType, argType)
			c.ctx.Diagnostics.Add(d)
			continue
		}
	}
}

// acceptImplicitCallArgument is the single semantic gate for method receivers
// and piped argument zero. Ordinary call arguments remain explicit.
func (c *checker) acceptImplicitCallArgument(scope *symbols.Scope, expr ast.Expr, argType, paramType types.Type) bool {
	refTarget, isMutable, isReference := types.ReferenceTarget(types.Underlying(paramType))
	if !isReference || !c.matchesImplicitCallTarget(refTarget, argType) {
		return false
	}
	isAddressable := place.IsAddressable(scope, expr, c.exprType, c.expandedDefaultBinding)
	var mutableBinding *symbols.Symbol
	if isMutable {
		isAddressable, _, mutableBinding = c.mutableAddressableExpr(scope, expr)
	}
	if isAddressable {
		if mutableBinding != nil {
			c.module.SymbolIndex.RequireMutable(mutableBinding)
		}
		return true
	}
	if isMutable {
		if site, msg, ok := c.mutableImplicitArgumentDiagnostic(scope, expr); ok {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment, msg, ast.LocOf(site), "immutable binding defined here")
			return true
		}
		return false
	}
	// Shared implicit arguments may borrow a temporary for this full expression.
	return true
}

func (c *checker) matchesImplicitCallTarget(target, arg types.Type) bool {
	if c.matchesReceiverTarget(target, arg) {
		return true
	}
	slice, sliceTarget := types.Underlying(target).(*types.ArrayType)
	array, isArrayArg := types.Underlying(arg).(*types.ArrayType)
	return sliceTarget && isArrayArg && slice != nil && array != nil &&
		slice.Shape == types.ArraySlice && array.Shape != types.ArraySlice &&
		types.IsSameType(slice.Elem, array.Elem)
}

func (c *checker) defaultCallDeclaration(callee ast.Expr) (*symbols.Symbol, *module.Module) {
	if c == nil || c.module == nil || c.module.SymbolIndex == nil || callee == nil {
		return nil, nil
	}
	sym := c.module.SymbolIndex.Symbol(callee)
	if sym == nil {
		return nil, nil
	}
	switch callee.(type) {
	case *ast.Ident:
		return sym, c.module
	case *ast.ScopeResolution:
		if owner, ok := c.ctx.ModuleByID(sym.DefiningModule); ok {
			return sym, owner
		}
	}
	return nil, nil
}

func (c *checker) expandCallDefaults(call *ast.CallExpr, args []ast.Expr, sym *symbols.Symbol, declModule *module.Module) []ast.Expr {
	effectiveArgs := append([]ast.Expr(nil), args...)
	if c == nil || c.module == nil || c.module.SymbolIndex == nil || c.evidence == nil || call == nil || sym == nil {
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
	for i := range provided {
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
		expanded, defaultClones, argumentClones := ast.SubstituteExpr(call.ID(), uint64(i), params[i].Default, substitutions)
		if declModule != nil && declModule.SymbolIndex != nil {
			for clonedID, originalID := range defaultClones {
				if resolved := declModule.SymbolIndex.SymbolID(originalID); resolved != nil {
					c.module.SymbolIndex.BindID(clonedID, resolved)
					c.evidence.MarkExpandedDefaultBinding(clonedID)
				}
				c.copyExpressionEvidence(declModule, clonedID, originalID)
			}
		}
		for clonedID, originalID := range argumentClones {
			if resolved := c.module.SymbolIndex.SymbolID(originalID); resolved != nil {
				c.module.SymbolIndex.BindID(clonedID, resolved)
			}
			if c.evidence.ExpandedDefaultBinding(originalID) {
				c.evidence.MarkExpandedDefaultBinding(clonedID)
			}
			c.copyExpressionEvidence(c.module, clonedID, originalID)
		}
		effectiveArgs = append(effectiveArgs, expanded)
		slotExprs[i] = expanded
		if params[i].Name != nil {
			substitutions[params[i].Name.Name] = expanded
		}
	}
	return effectiveArgs
}

func (c *checker) copyExpressionEvidence(src *module.Module, dstID, srcID source.NodeID) {
	if c == nil || c.evidence == nil || src == nil || !dstID.IsValid() || !srcID.IsValid() {
		return
	}
	if src == c.module {
		c.evidence.CloneReusableExpressionEvidenceFrom(dstID, c.evidence, srcID)
		return
	}
	if src.THIR == nil {
		return
	}
	expr, ok := src.THIR.Node(srcID).(thir.Expr)
	if !ok || expr == nil {
		return
	}
	if typ := expr.ExprType(); typ != nil {
		c.evidence.RecordExprType(dstID, typ)
	}
	c.evidence.RecordInterfaceImplementations(dstID, expr.InterfaceImplementations())
	if conversion := expr.Conversion(); conversion != nil {
		c.evidence.RecordImplicitConversion(dstID, *conversion)
	}
	if field, ok := expr.(*thir.Field); ok && field.Access != nil {
		c.evidence.RecordStructField(dstID, StructFieldAccess{
			Field: field.Access.Field, Type: field.ExprType(), DereferenceType: field.Access.DereferenceType,
		})
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

func (c *checker) callReturnType(call *ast.CallExpr, calleeType types.Type) types.Type {
	if c == nil {
		return &types.InvalidType{}
	}
	if calleeType != nil {
		if fnType, ok := calleeType.(*types.FuncType); ok && fnType != nil {
			if fnType.Return == nil {
				return nil
			}
			if !types.IsUnknown(fnType.Return) {
				return fnType.Return
			}
			if call != nil {
				c.ctx.Diagnostics.Add(invalidTypeError(call, "call has unknown return type"))
			}
			return &types.InvalidType{}
		}
	}
	return &types.InvalidType{}
}
