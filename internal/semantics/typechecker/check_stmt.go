package typechecker

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/flowresult"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func (c *checker) checkBlock(parentScope *symbols.Scope, block *ast.BlockStmt, returnType typeinfo.Type) {
	if block == nil {
		return
	}
	scope := parentScope
	if c.module.Semantics != nil {
		if s, ok := c.module.Semantics.BlockScopes[block.ID()]; ok && s != nil {
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
		if node.Cond != nil {
			condType := c.typeExpr(scope, node.Cond, nil)
			if condType != nil && !typeinfo.IsInvalidOrUnknown(condType) && !typeinfo.IsCondition(condType) {
				c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node.Cond, "for condition must be bool"))
			}
		}
		if c.siteOnly {
			return
		}
		c.checkBlock(scope, node.Body, returnType)
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
	if typeinfo.NeedsDrop(subjectType) && !place.IsPlaceExpr(node.Subject) {
		c.ctx.Diagnostics.Add(invalidOperationError(node.Subject,
			"ownership-bearing match subject must be a named place").
			WithHelp("bind subject to a local before matching it"))
	}
	evidence := flowresult.Match{
		SubjectID: node.Subject.ID(),
		EnumType:  subjectType,
		Cases:     append([]typeinfo.VariantCase(nil), descriptor.Cases...),
		Arms:      make([]flowresult.MatchArm, 0, len(node.Arms)),
	}
	seenCases := make(map[int]ast.Node, len(node.Arms))
	for _, arm := range node.Arms {
		if arm == nil || arm.Case == nil {
			evidenceComplete = false
			continue
		}
		armEvidence := flowresult.MatchArm{ArmID: arm.ID(), Case: -1}
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
				fieldEvidence := flowresult.MatchField{WholePayload: true, Type: resolved.Case.Payload, Discard: arm.Discard}
				if arm.Binding != nil {
					fieldEvidence.Binding = c.module.Semantics.ResolvedSymbols[arm.Binding.ID()]
					if fieldEvidence.Binding != nil {
						fieldEvidence.Binding.BindType(resolved.Case.Payload)
					}
				}
				armEvidence.Fields = append(armEvidence.Fields, fieldEvidence)
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
						fieldEvidence := flowresult.MatchField{Field: fieldIndex, Type: field.Type, Discard: pattern.Discard}
						if !pattern.Discard && pattern.Binding != nil {
							fieldEvidence.Binding = c.module.Semantics.ResolvedSymbols[pattern.Binding.ID()]
							if fieldEvidence.Binding != nil {
								fieldEvidence.Binding.BindType(field.Type)
							}
						}
						armEvidence.Fields = append(armEvidence.Fields, fieldEvidence)
					}
				}
			}
		}
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
		c.module.Semantics.Matches[node.ID()] = evidence
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
		var sharedReference typeinfo.Type
		if refTarget, mutable, ok := typeinfo.ReferenceTarget(typeinfo.Underlying(baseType)); ok {
			if mutable {
				return
			}
			sharedReference = refTarget
		} else {
			var mutable bool
			mutable, sharedReference = c.mutableAddressableExpr(scope, target.Expr)
			if mutable {
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
	if mutable, _ := c.mutableAddressableExpr(scope, target.Expr); mutable {
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
	if c.module == nil || c.module.Semantics == nil {
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
	if c == nil || c.module == nil || c.module.Semantics == nil || expr == nil {
		return nil
	}
	exprType := func(node ast.Expr) typeinfo.Type {
		if node == nil {
			return nil
		}
		return c.module.Semantics.ExprTypes[node.ID()]
	}
	if _, _, reference := typeinfo.ReferenceValueTarget(exprType(expr)); !reference {
		return nil
	}
	switch node := expr.(type) {
	case *ast.AddressExpr:
		if node == nil || node.Expr == nil || node.Mode == ast.AddressRaw || place.Addressable(scope, node.Expr, exprType, c.expandedDefaultBinding) {
			return nil
		}
		return node
	case *ast.CallExpr:
		fn, _ := typeinfo.Underlying(exprType(node.Callee)).(*typeinfo.FuncType)
		for _, source := range typeinfo.ReturnOriginSources(node, fn) {
			if temporary := c.temporaryBorrowSource(scope, source); temporary != nil {
				return temporary
			}
			if c.module.Semantics.ImplicitCallArguments[source.ID()] != nil && !place.Addressable(scope, source, exprType, c.expandedDefaultBinding) {
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
		if !place.Addressable(scope, node.Expr, exprType, c.expandedDefaultBinding) {
			return node
		}
	case *ast.IndexExpr:
		if temporary := c.temporaryBorrowSource(scope, node.Expr); temporary != nil {
			return temporary
		}
		if !place.Addressable(scope, node.Expr, exprType, c.expandedDefaultBinding) {
			return node
		}
	}
	return nil
}
