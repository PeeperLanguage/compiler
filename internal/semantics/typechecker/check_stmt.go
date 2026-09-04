package typechecker

import (
	"fmt"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

func (c *checker) checkBlock(parentScope *symbols.Scope, block *ast.BlockStmt, returnType typeinfo.Type) {
	if block == nil {
		return
	}
	scope := parentScope
	if c.module.Bindings != nil {
		if s, ok := c.module.Bindings.BlockScopes[block.ID()]; ok && s != nil {
			scope = s
		}
	}
	for _, stmt := range block.Stmts {
		c.checkStmt(scope, stmt, returnType)
	}
}

func (c *checker) checkStmt(scope *symbols.Scope, stmt ast.Stmt, returnType typeinfo.Type) {
	if stmt == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		c.checkBlock(scope, node, returnType)
	case *ast.LetDecl:
		c.checkBinding(scope, node, false)
	case *ast.ConstDecl:
		c.checkBinding(scope, node, true)
	case *ast.ReturnStmt:
		if node.Value == nil {
			if returnType != nil {
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn, "return value required", ast.LocOf(node), "")
			}
			return
		}
		if returnType == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn, "cannot return a value from function with no return type", ast.LocOf(node.Value), "")
			return
		}
		retType := c.requireValueType(node.Value, c.typeExpr(scope, node.Value, returnType), "return")
		if typeinfo.IsInvalidOrUnknown(retType) {
			return
		}
		if c.rejectTemporaryBorrowEscape(scope, node.Value, "return") {
			return
		}
		if !c.assignable(returnType, retType, node.Value) {
			d := typeMismatchError(node.Value,
				fmt.Sprintf("cannot return %s from function returning %s",
					typeinfo.TypeText(retType), typeinfo.TypeText(returnType)))
			if fn := c.enclosingFnDecl(scope); fn != nil && fn.ReturnType != nil {
				d.WithSecondaryLabel(ast.LocOf(fn.ReturnType),
					fmt.Sprintf("expected %s here", typeinfo.TypeText(returnType)))
			}
			c.addInterfaceHint(d, returnType, retType)
			c.ctx.Diagnostics.Add(d)
		}
	case *ast.IfStmt:
		if node.Cond == nil {
			return // resolver already diagnosed missing condition
		}
		condType := c.typeExpr(scope, node.Cond, nil)
		if condType != nil && !typeinfo.IsInvalidOrUnknown(condType) && !typeinfo.IsCondition(condType) {
			c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node.Cond, "if condition must be bool"))
		}
		if c.siteOnly {
			return
		}
		c.checkBlock(scope, node.Then, returnType)
		c.checkStmt(scope, node.Else, returnType)
	case *ast.ForStmt:
		if node.Iterable != nil {
			c.checkForInStmt(scope, node, returnType)
			return
		}
		if node.Cond != nil {
			condType := c.typeExpr(scope, node.Cond, nil)
			if condType != nil && !typeinfo.IsInvalidOrUnknown(condType) && !typeinfo.IsCondition(condType) {
				c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node.Cond, "for condition must be bool"))
			}
		}
		if c.siteOnly {
			return
		}
		c.loopDepth++
		c.checkBlock(scope, node.Body, returnType)
		c.loopDepth--
	case *ast.BreakStmt, *ast.ContinueStmt:
		if c.siteOnly {
			return
		}
		if c.loopDepth == 0 {
			jump := "break"
			if _, ok := stmt.(*ast.ContinueStmt); ok {
				jump = "continue"
			}
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
				jump+" outside loop", ast.LocOf(stmt), "`"+jump+"` exits or restarts the innermost for loop")
		}
	case *ast.MatchStmt:
		c.checkMatchStmt(scope, node, returnType)
	case *ast.ExprStmt:
		if node.Expr == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
				"expression statement requires an expression", ast.LocOf(node), "")
			return
		}
		c.typeExpr(scope, node.Expr, nil)
	case *ast.AssignStmt:
		c.checkAssign(scope, node)
	case *ast.BadStmt, *ast.BadDecl, *ast.ImportDecl, *ast.FnDecl,
		*ast.TypeAliasDecl, *ast.StructDecl, *ast.InterfaceDecl, *ast.EnumDecl:
		return // resolver already diagnosed unsupported statements
	default:
		panic(fmt.Sprintf("typechecker: unhandled statement %T", stmt))
	}
}

func (c *checker) checkMatchStmt(scope *symbols.Scope, node *ast.MatchStmt, returnType typeinfo.Type) {
	if c == nil || scope == nil || node == nil || node.Subject == nil {
		return
	}
	subjectType := c.requireValueType(node.Subject, c.typeWholeCarrierExpr(scope, node.Subject, nil), "match subject")
	if c.siteOnly || typeinfo.IsInvalidOrUnknown(subjectType) {
		return
	}
	descriptor, named := typeinfo.VariantDescriptorOf(subjectType)
	if !named || descriptor.Family != typeinfo.VariantFamilyNamed {
		c.ctx.Diagnostics.Add(invalidOperationError(node.Subject,
			"match subject must be a named enum, got "+typeinfo.TypeText(subjectType)))
		return
	}
	evidenceComplete := true
	if typeinfo.OwnershipCapabilityOf(subjectType).Drop && !place.IsPlaceExpr(node.Subject) {
		c.ctx.Diagnostics.Add(invalidOperationError(node.Subject,
			"ownership-bearing match subject must be a named place").
			WithHelp("bind subject to a local before matching it"))
	}
	evidence := typecheckresult.Match{
		SubjectID: node.Subject.ID(),
		EnumType:  subjectType,
		CaseCount: len(descriptor.Cases),
		Arms:      make([]typecheckresult.MatchArm, 0, len(node.Arms)),
	}
	seenCases := make(map[int]ast.Node, len(node.Arms))
	for _, arm := range node.Arms {
		if arm == nil || arm.Case == nil {
			evidenceComplete = false
			continue
		}
		armEvidence := typecheckresult.MatchArm{ArmID: arm.ID(), Case: -1}
		if arm.Body != nil {
			armEvidence.BodyID = arm.Body.ID()
		}
		resolved, pathOK := c.resolveNamedVariant(arm.Case)
		if !pathOK {
			evidenceComplete = false
			c.checkBlock(scope, arm.Body, returnType)
			continue
		}
		if typeinfo.IsInvalidOrUnknown(resolved.EnumType) {
			evidenceComplete = false
			c.checkBlock(scope, arm.Body, returnType)
			continue
		}
		if !typeinfo.SameType(subjectType, resolved.EnumType) {
			evidenceComplete = false
			c.ctx.Diagnostics.Add(typeMismatchError(arm.Case,
				fmt.Sprintf("match arm requires %s, got %s", typeinfo.TypeText(subjectType), typeinfo.TypeText(resolved.EnumType))))
			c.checkBlock(scope, arm.Body, returnType)
			continue
		}
		armEvidence.Case = resolved.CaseIndex
		if previous := seenCases[resolved.CaseIndex]; previous != nil {
			c.ctx.Diagnostics.Add(diagnostics.NewError("duplicate match arm for `"+resolved.CaseName.Name+"`").
				WithCode(diagnostics.ErrInvalidStatement).
				WithPrimaryLabel(ast.LocOf(arm.Case), "duplicate case").
				WithSecondaryLabel(ast.LocOf(previous), "first matched here"))
		} else {
			seenCases[resolved.CaseIndex] = arm.Case
		}
		if resolved.Case.Payload == nil {
			if arm.HasData {
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
					"payloadless match case `"+resolved.CaseName.Name+"` does not accept a payload", ast.LocOf(arm), "remove `with` and its pattern")
			}
		} else {
			armEvidence.Payload = resolved.Case.Payload
			if !arm.HasData {
				c.ctx.Diagnostics.AddError(diagnostics.ErrMissingInitializer,
					"data match case `"+resolved.CaseName.Name+"` requires a payload pattern", ast.LocOf(arm), "add `with <binding>` or `with _`")
			} else if arm.Binding != nil || arm.Discard {
				fieldEvidence := typecheckresult.MatchBinding{Projection: typecheckresult.MatchWholePayload, Type: resolved.Case.Payload, Discard: arm.Discard}
				if arm.Binding != nil {
					fieldEvidence.Binding = c.module.Bindings.NodeSymbols[arm.Binding.ID()]
					if fieldEvidence.Binding != nil {
						fieldEvidence.Binding.BindType(resolved.Case.Payload)
					}
				}
				armEvidence.Bindings = append(armEvidence.Bindings, fieldEvidence)
			} else {
				payload, payloadOK := typeinfo.Underlying(resolved.Case.Payload).(*typeinfo.StructType)
				if !payloadOK || payload == nil {
					c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
						"field pattern requires a struct payload", ast.LocOf(arm), "write `with <binding>` or `with _`")
				} else {
					seenFields := make(map[string]ast.Node, len(arm.Fields))
					for _, pattern := range arm.Fields {
						if pattern.Name == nil {
							continue
						}
						name := pattern.Name.Name
						if previous := seenFields[name]; previous != nil {
							c.ctx.Diagnostics.Add(diagnostics.NewError("duplicate match pattern field `"+name+"`").
								WithCode(diagnostics.ErrDuplicateField).
								WithPrimaryLabel(ast.LocOf(pattern.Name), "duplicate field").
								WithSecondaryLabel(ast.LocOf(previous), "first listed here"))
							continue
						}
						seenFields[name] = pattern.Name
						field, fieldIndex, fieldFound := typeinfo.LookupStructField(payload, name)
						if !fieldFound {
							c.ctx.Diagnostics.AddError(diagnostics.ErrFieldNotFound,
								"unknown match pattern field `"+name+"`", ast.LocOf(pattern.Name), "")
							continue
						}
						fieldEvidence := typecheckresult.MatchBinding{Projection: typecheckresult.MatchPayloadField, Field: fieldIndex, Type: field.Type, Discard: pattern.Discard}
						if !pattern.Discard && pattern.Binding != nil {
							fieldEvidence.Binding = c.module.Bindings.NodeSymbols[pattern.Binding.ID()]
							if fieldEvidence.Binding != nil {
								fieldEvidence.Binding.BindType(field.Type)
							}
						}
						armEvidence.Bindings = append(armEvidence.Bindings, fieldEvidence)
					}
				}
			}
		}
		carrierUse := typeinfo.UseRead
		for _, field := range armEvidence.Bindings {
			if typeinfo.OwnershipCapabilityOf(field.Type).Copy != typeinfo.CopyImplicit {
				carrierUse = typeinfo.UseMove
				break
			}
		}
		armEvidence.CarrierUse = carrierUse
		evidence.Arms = append(evidence.Arms, armEvidence)
		c.checkBlock(scope, arm.Body, returnType)
	}
	for caseIndex, variant := range descriptor.Cases {
		if seenCases[caseIndex] != nil {
			continue
		}
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidStatement,
			"match is missing case `"+variant.Name+"`", ast.LocOf(node), "add one arm for every enum case")
	}
	if evidenceComplete && len(evidence.Arms) == len(node.Arms) {
		c.module.Typechecking.Matches[node.ID()] = evidence
	}
}

func (c *checker) checkAssign(scope *symbols.Scope, node *ast.AssignStmt) {
	if c == nil || scope == nil || node == nil || node.Target == nil || node.Value == nil {
		return
	}
	targetType := c.typeWholeCarrierExpr(scope, node.Target, nil)
	if targetType == nil || typeinfo.IsInvalidOrUnknown(targetType) {
		return
	}
	valueType := c.typeExpr(scope, node.Value, targetType)
	valueType = c.requireValueType(node.Value, valueType, "assignment")
	if typeinfo.IsInvalidOrUnknown(valueType) {
		return
	}
	if c.rejectTemporaryBorrowEscape(scope, node.Value, "assignment") {
		return
	}
	if !c.assignable(targetType, valueType, node.Value) {
		c.ctx.Diagnostics.Add(typeMismatchError(node.Value,
			fmt.Sprintf("cannot assign %s to %s",
				typeinfo.TypeText(valueType), typeinfo.TypeText(targetType))))
		return
	}
	switch target := node.Target.(type) {
	case *ast.Ident:
		sym, ok := scope.Lookup(target.Name)
		if !ok || sym == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol,
				"unknown assignment target `"+target.Name+"`", ast.LocOf(target), "")
			return
		}
		switch sym.Kind {
		case symbols.SymbolConst:
			c.ctx.Diagnostics.AddError(diagnostics.ErrConstantReassignment,
				"cannot assign to const `"+target.Name+"`", ast.LocOf(target), "").
				WithSecondaryLabel(sym.Location, "declared as const here")
			return
		case symbols.SymbolVar, symbols.SymbolParam:
			if !sym.IsMutable() {
				c.ctx.Diagnostics.AddError(
					diagnostics.ErrInvalidAssignment,
					"modification to immutable symbol",
					ast.LocOf(target),
					"cannot assign to immutable binding `"+target.Name+"`",
				).WithSecondaryLabel(sym.Location, "make this binding mutable")
				return
			}
			sym.RequiresMutable = true
		default:
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
				"invalid assignment target `"+target.Name+"`", ast.LocOf(target), "")
			return
		}
	case *ast.SelectorExpr:
		baseType := c.typeExpr(scope, target.Expr, nil)
		if _, ok := typeinfo.PointerTarget(typeinfo.Underlying(baseType)); ok {
			return
		}
		var (
			sharedReference typeinfo.Type
			mutableBinding  *symbols.Symbol
		)
		if refTarget, mutable, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(baseType)); ok {
			if mutable {
				return
			}
			sharedReference = refTarget
		} else {
			var mutable bool
			mutable, sharedReference, mutableBinding = c.mutableAddressableExpr(scope, target.Expr)
			if mutable {
				if mutableBinding != nil {
					mutableBinding.RequiresMutable = true
				}
				return
			}
		}
		if sharedReference != nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
				"cannot assign through immutable reference", ast.LocOf(target), "").
				WithHelp(fmt.Sprintf("use `&mut %s` to modify referenced value", typeinfo.TypeText(sharedReference)))
			return
		}
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"field assignment requires a mutable pointer, reference, or local binding", ast.LocOf(target), "")
		return
	case *ast.IndexExpr:
		if c.checkIndexAssignmentTarget(scope, target, targetType) {
			return
		}
		return
	default:
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"invalid assignment target", ast.LocOf(node.Target), "")
	}
}

func (c *checker) checkIndexAssignmentTarget(scope *symbols.Scope, target *ast.IndexExpr, targetType typeinfo.Type) bool {
	if c == nil || target == nil || target.Expr == nil {
		return false
	}
	if typeinfo.IsInvalidOrUnknown(targetType) {
		return true
	}
	baseType := c.typeExpr(scope, target.Expr, nil)
	if typeinfo.IsInvalidOrUnknown(baseType) {
		return true
	}
	_, shape, ok := indexableSequence(baseType)
	if !ok {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"index assignment requires array or slice value", ast.LocOf(target), "")
		return false
	}
	if shape == indexableSharedSliceView {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"index assignment requires mutable array or mutable slice view", ast.LocOf(target), "")
		return false
	}
	if shape == indexableMutableSliceView {
		return true
	}
	if mutable, _, mutableBinding := c.mutableAddressableExpr(scope, target.Expr); mutable {
		if mutableBinding != nil {
			mutableBinding.RequiresMutable = true
		}
		return true
	}
	c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
		"index assignment requires mutable array or slice binding", ast.LocOf(target), "")
	return false
}

func (c *checker) checkBinding(scope *symbols.Scope, node ast.Stmt, requireInitializer bool) {
	if c == nil || node == nil {
		return
	}
	var (
		declType typeinfo.Type
		typeNode ast.TypeExpr // AST node for the type annotation (for diagnostics)
		value    ast.Expr
	)
	switch bind := node.(type) {
	case *ast.LetDecl:
		declType = typeinfo.TypeFromSyntax(bind.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
		typeNode = bind.Type
		value = bind.Value
	case *ast.ConstDecl:
		declType = typeinfo.TypeFromSyntax(bind.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
		typeNode = bind.Type
		value = bind.Value
	default:
		return
	}

	// Look up the symbol declared in this exact scope by the resolver.
	sym, found := scope.LookupNode(node)
	if !found || sym == nil {
		return
	}
	if declType != nil && c.rejectUnsizedType(declType, typeNode, "binding") {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}

	if value == nil {
		if requireInitializer {
			sym.BindType(&typeinfo.InvalidType{})
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingInitializer,
				"missing initializer for const declaration", ast.LocOf(node), "")
			return
		}
		if declType == nil {
			sym.BindType(&typeinfo.InvalidType{})
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingType,
				"let declaration needs type or initializer", ast.LocOf(node), "")
			return
		}
		if c.rejectBindingReferenceStorage(scope, declType, typeNode) {
			sym.BindType(&typeinfo.InvalidType{})
			return
		}
		sym.BindType(declType)
		return
	}
	if declType != nil && c.rejectBindingReferenceStorage(scope, declType, typeNode) {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}

	valType := c.typeExpr(scope, value, declType)
	valType = c.requireValueType(value, valType, "initializer")
	if typeinfo.IsInvalidOrUnknown(valType) {
		if declType != nil && !typeinfo.IsInvalidOrUnknown(declType) {
			sym.BindType(declType)
		} else {
			sym.BindType(&typeinfo.InvalidType{})
		}
		return
	}
	if c.rejectTemporaryBorrowEscape(scope, value, "binding") {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}
	if declType == nil && c.rejectBindingReferenceStorage(scope, valType, node) {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}
	if declType == nil && c.rejectUnsizedType(valType, node, "binding") {
		sym.BindType(&typeinfo.InvalidType{})
		return
	}
	if declType != nil && !c.assignable(declType, valType, value) {
		d := typeMismatchError(value,
			fmt.Sprintf("cannot assign %s to %s",
				typeinfo.TypeText(valType), typeinfo.TypeText(declType)))
		if typeNode != nil {
			d.WithSecondaryLabel(ast.LocOf(typeNode),
				fmt.Sprintf("expected %s because of this type annotation", typeinfo.TypeText(declType)))
		}
		c.addInterfaceHint(d, declType, valType)
		c.ctx.Diagnostics.Add(d)
		if !typeinfo.IsInvalidOrUnknown(declType) {
			sym.BindType(declType)
		} else {
			sym.BindType(&typeinfo.InvalidType{})
		}
		return
	}
	if declType != nil {
		sym.BindType(declType)
	} else {
		sym.BindType(valType)
	}
}

// checkForInStmt types a `for x in iterable` loop. The iterable may be a range
// expression or an indexable sequence; strings are rejected because string
// element access requires an explicit as_bytes/as_chars view.
func (c *checker) checkForInStmt(scope *symbols.Scope, node *ast.ForStmt, returnType typeinfo.Type) {
	indexType := typeinfo.DefaultIntegerType()
	evidence := typecheckresult.ForIteration{}
	if node.Index != nil {
		evidence.Index = c.module.Bindings.NodeSymbols[node.Index.ID()]
	}
	if node.Value != nil {
		evidence.Value = c.module.Bindings.NodeSymbols[node.Value.ID()]
	}
	valid := node.Value != nil && node.Value.Name != "" && evidence.Value != nil
	if node.Index != nil && (node.Index.Name == "" || evidence.Index == nil) {
		valid = false
	}

	var elemType typeinfo.Type
	var carrierType typeinfo.Type
	rangeExpr, isRange := node.Iterable.(*ast.RangeExpr)
	if isRange {
		if !rangeExpr.EndExclusive {
			valid = false
			c.ctx.Diagnostics.Add(invalidExpressionError(rangeExpr, "for range requires an exclusive end; use `..` instead of `..=`"))
		}
		_, badStart := rangeExpr.Start.(*ast.BadExpr)
		if rangeExpr.Start == nil || badStart {
			valid = false
			c.ctx.Diagnostics.Add(invalidExpressionError(rangeExpr, "range iteration requires a start bound"))
		}
		_, badEnd := rangeExpr.End.(*ast.BadExpr)
		if rangeExpr.End == nil || badEnd {
			valid = false
			c.ctx.Diagnostics.Add(invalidExpressionError(rangeExpr, "range iteration requires an end bound"))
		}

		defaultType := typeinfo.DefaultIntegerType()
		startNumber, startLiteral := rangeExpr.Start.(*ast.NumberLit)
		endNumber, endLiteral := rangeExpr.End.(*ast.NumberLit)
		startUntyped := startLiteral && startNumber.ExplicitType == ""
		endUntyped := endLiteral && endNumber.ExplicitType == ""
		var startType, endType typeinfo.Type
		if startUntyped && !endUntyped {
			endType = c.checkRangeBound(scope, rangeExpr.End, defaultType)
			startType = c.checkRangeBound(scope, rangeExpr.Start, endType)
		} else {
			startType = c.checkRangeBound(scope, rangeExpr.Start, defaultType)
			endExpected := defaultType
			if endUntyped && !typeinfo.IsInvalidOrUnknown(startType) {
				endExpected = startType
			}
			endType = c.checkRangeBound(scope, rangeExpr.End, endExpected)
		}
		if typeinfo.IsInvalidOrUnknown(startType) || typeinfo.IsInvalidOrUnknown(endType) ||
			!typeinfo.IsIntegral(startType) || !typeinfo.IsIntegral(endType) {
			valid = false
		} else {
			elemType = typeinfo.CommonNumericType(startType, endType)
			if elemType == nil || !typeinfo.IsIntegral(elemType) {
				valid = false
				c.ctx.Diagnostics.Add(typeMismatchError(rangeExpr,
					"range bounds of type "+typeinfo.TypeText(startType)+" and "+typeinfo.TypeText(endType)+" have no common integer type"))
			} else {
				if !c.assignable(elemType, startType, rangeExpr.Start) || !c.assignable(elemType, endType, rangeExpr.End) {
					valid = false
				}
			}
		}
		if valid {
			startValue, startFound := consteval.EvaluateExpr(c.ctx, c.module, scope, rangeExpr.Start, elemType)
			endValue, endFound := consteval.EvaluateExpr(c.ctx, c.module, scope, rangeExpr.End, elemType)
			start, startIntegral := startValue.(*constvalue.IntConst)
			end, endIntegral := endValue.(*constvalue.IntConst)
			if startFound && endFound && startIntegral && endIntegral && start.Int().Cmp(end.Int()) < 0 {
				evidence.GuaranteedEntry = true
			}
		}
		evidence.ElementType = elemType
	} else {
		var ok bool
		indexType, ok = typeinfo.NumericTypeFromName("usize", c.ctx.Target)
		if !ok {
			panic("missing builtin usize type")
		}
		iterableType := c.typeExpr(scope, node.Iterable, nil)
		iterableType = c.requireValueType(node.Iterable, iterableType, "iterable")

		if typeinfo.IsInvalidOrUnknown(iterableType) {
			valid = false
		} else if isStringSequence(iterableType) {
			valid = false
			c.ctx.Diagnostics.Add(invalidExpressionError(node.Iterable,
				"string iteration requires `value |> as_bytes()` or `value |> as_chars()`"))
		} else if elem, shape, ok := indexableSequence(iterableType); ok {
			elemType = elem
			if shape == indexableFixedArray || shape == indexableDynamicArray {
				exprType := func(expr ast.Expr) typeinfo.Type {
					return c.typeExpr(scope, expr, nil)
				}
				if !place.Addressable(scope, node.Iterable, exprType, c.module.ExpandedDefaultBinding) {
					valid = false
					c.ctx.Diagnostics.Add(invalidExpressionError(node.Iterable,
						"for-in requires addressable array storage"))
				}
			}
			if typeinfo.OwnershipCapabilityOf(elem).Copy != typeinfo.CopyImplicit {
				valid = false
				c.ctx.Diagnostics.Add(invalidExpressionError(node.Iterable,
					"for-in requires copyable sequence elements; iterate indexes and borrow move-only elements explicitly"))
			}
			if _, array := typeinfo.Underlying(iterableType).(*typeinfo.ArrayType); array {
				carrierType = &typeinfo.RefType{Target: iterableType}
			} else {
				carrierType = iterableType
			}
			evidence.ElementType = elem
		} else {
			valid = false
			c.ctx.Diagnostics.Add(invalidExpressionError(node.Iterable,
				"cannot iterate over "+typeinfo.TypeText(iterableType)))
		}
	}
	if node.Index != nil {
		c.bindLoopVariable(node.Index, indexType)
	}
	if node.Value != nil {
		c.bindLoopVariable(node.Value, elemType)
	}
	if c.siteOnly {
		return
	}
	delete(c.module.Typechecking.ForIterations, node.ID())
	if valid && elemType != nil && !typeinfo.IsInvalidOrUnknown(elemType) {
		location := ast.LocOf(node)
		evidence.Cursor = symbols.New("$for.cursor", symbols.SymbolVar, nil, location)
		if isRange {
			evidence.Cursor.BindType(elemType)
			plan := &typecheckresult.RangeIteration{
				Limit: symbols.New("$for.end", symbols.SymbolVar, nil, location),
			}
			plan.Limit.BindType(elemType)
			if node.Index != nil {
				plan.Ordinal = symbols.New("$for.ordinal", symbols.SymbolVar, nil, location)
				plan.Ordinal.BindType(indexType)
			}
			evidence.Plan = plan
		} else {
			evidence.Cursor.BindType(indexType)
			carrier := symbols.New("$for.carrier", symbols.SymbolVar, nil, location)
			carrier.BindType(carrierType)
			evidence.Plan = &typecheckresult.SequenceIteration{Carrier: carrier, CarrierType: carrierType}
		}
		c.module.Typechecking.ForIterations[node.ID()] = evidence
	}
	c.loopDepth++
	c.checkBlock(scope, node.Body, returnType)
	c.loopDepth--
}

func (c *checker) bindLoopVariable(name *ast.Ident, typ typeinfo.Type) {
	if typ == nil {
		return
	}
	if sym := c.module.Bindings.NodeSymbols[name.ID()]; sym != nil {
		sym.BindType(typ)
	}
}

func (c *checker) rejectUnsizedType(typ typeinfo.Type, site ast.Node, context string) bool {
	if typeinfo.IsSizedType(typ) {
		return false
	}
	diagnostic := invalidTypeError(site,
		fmt.Sprintf("%s requires a sized type; %s is unsized", context, typeinfo.TypeText(typ)))
	if _, ok := typeinfo.Underlying(typ).(*typeinfo.InterfaceType); ok {
		diagnostic.WithHelp("use &Interface, &mut Interface, or *Interface instead of a bare interface value")
	}
	c.ctx.Diagnostics.Add(diagnostic)
	return true
}

func (c *checker) rejectBindingReferenceStorage(scope *symbols.Scope, typ typeinfo.Type, site ast.Node) bool {
	moduleBinding := c != nil && c.module != nil && scope == c.module.ModuleScope
	if !moduleBinding {
		if descriptor, variant := typeinfo.VariantDescriptorOf(typ); variant && descriptor.Family == typeinfo.VariantFamilyNamed {
			for _, variantCase := range descriptor.Cases {
				payload, _ := typeinfo.Underlying(variantCase.Payload).(*typeinfo.StructType)
				if payload == nil {
					continue
				}
				for _, field := range payload.Fields {
					if c.rejectReferenceStorage(field.Type, site, "named enum payloads", false) {
						return true
					}
				}
			}
			return false
		}
	}
	context := "array or heap-owned values"
	if moduleBinding {
		context = "module bindings"
	}
	return c.rejectReferenceStorage(typ, site, context, moduleBinding)
}

func (c *checker) rejectReferenceStorage(typ typeinfo.Type, site ast.Node, context string, includeDirectReference bool) bool {
	rejected := typeinfo.ContainsStoredReference(typ)
	if includeDirectReference {
		rejected = typeinfo.ContainsReference(typ)
	}
	if !rejected {
		return false
	}
	c.ctx.Diagnostics.Add(invalidTypeError(site,
		fmt.Sprintf("references cannot be stored in %s in v1", context)))
	return true
}

func (c *checker) rejectTemporaryBorrowEscape(scope *symbols.Scope, expr ast.Expr, context string) bool {
	if c.module == nil {
		return false
	}
	temporary := c.temporaryBorrowSource(scope, expr)
	if temporary == nil {
		return false
	}
	diagnostic := c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
		fmt.Sprintf("reference to temporary cannot escape through %s", context), ast.LocOf(expr), "temporary borrow escapes here").
		WithHelp("pass the borrow directly to a call so it ends with the full expression")
	if temporary != expr {
		diagnostic.WithSecondaryLabel(ast.LocOf(temporary), "temporary borrowed here")
	}
	return true
}

func (c *checker) temporaryBorrowSource(scope *symbols.Scope, expr ast.Expr) ast.Expr {
	if c == nil || c.module == nil || expr == nil {
		return nil
	}
	exprType := func(node ast.Expr) typeinfo.Type {
		if node == nil {
			return nil
		}
		return c.module.BaseExprType(node.ID())
	}
	if _, _, reference := typeinfo.ReferenceValueTarget(exprType(expr)); !reference {
		return nil
	}
	switch node := expr.(type) {
	case *ast.AddressExpr:
		if node == nil || node.Expr == nil || node.Mode == ast.AddressRaw || place.Addressable(scope, node.Expr, exprType, c.module.ExpandedDefaultBinding) {
			return nil
		}
		return node
	case *ast.CallExpr:
		fn, _ := typeinfo.Underlying(exprType(node.Callee)).(*typeinfo.FuncType)
		args := c.module.Typechecking.CallArgumentsOrSource(node)
		for _, source := range typeinfo.ReturnOriginSources(node, args, fn) {
			if temporary := c.temporaryBorrowSource(scope, source); temporary != nil {
				return temporary
			}
			if c.module.Typechecking.ImplicitCallArguments[source.ID()] != nil && !place.Addressable(scope, source, exprType, c.module.ExpandedDefaultBinding) {
				if _, _, reference := typeinfo.ReferenceValueTarget(exprType(source)); !reference {
					return source
				}
			}
		}
	case *ast.AsExpr:
		return c.temporaryBorrowSource(scope, node.Expr)
	case *ast.SelectorExpr:
		if temporary := c.temporaryBorrowSource(scope, node.Expr); temporary != nil {
			return temporary
		}
		if !place.Addressable(scope, node.Expr, exprType, c.module.ExpandedDefaultBinding) {
			return node
		}
	case *ast.IndexExpr:
		if temporary := c.temporaryBorrowSource(scope, node.Expr); temporary != nil {
			return temporary
		}
		if !place.Addressable(scope, node.Expr, exprType, c.module.ExpandedDefaultBinding) {
			return node
		}
	}
	return nil
}
