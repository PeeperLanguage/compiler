package typechecker

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
)

func (c *checker) checkBlock(parentScope *table.Scope, block *ast.BlockStmt, returnType typeinfo.Type) {
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

func (c *checker) checkStmt(scope *table.Scope, stmt ast.Stmt, returnType typeinfo.Type) {
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
		c.checkBlock(scope, node.Then, returnType)
		c.checkStmt(scope, node.Else, returnType)
	case *ast.ForStmt:
		if node.Cond != nil {
			condType := c.typeExpr(scope, node.Cond, nil)
			if condType != nil && !typeinfo.IsInvalidOrUnknown(condType) && !typeinfo.IsCondition(condType) {
				c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node.Cond, "for condition must be bool"))
			}
		}
		c.checkBlock(scope, node.Body, returnType)
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

func (c *checker) checkAssign(scope *table.Scope, node *ast.AssignStmt) {
	if c == nil || scope == nil || node == nil || node.Target == nil || node.Value == nil {
		return
	}
	targetType := c.typeExpr(scope, node.Target, nil)
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

func (c *checker) checkIndexAssignmentTarget(scope *table.Scope, target *ast.IndexExpr, targetType typeinfo.Type) bool {
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

func (c *checker) checkBinding(scope *table.Scope, node ast.Stmt, requireInitializer bool) {
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

func (c *checker) rejectBindingReferenceStorage(scope *table.Scope, typ typeinfo.Type, site ast.Node) bool {
	moduleBinding := c != nil && c.module != nil && scope == c.module.ModuleScope
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

func (c *checker) rejectTemporaryBorrowEscape(scope *table.Scope, expr ast.Expr, context string) bool {
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

func (c *checker) temporaryBorrowSource(scope *table.Scope, expr ast.Expr) ast.Expr {
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
