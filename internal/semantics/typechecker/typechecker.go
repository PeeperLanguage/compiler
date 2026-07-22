package typechecker

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/problems"
	"compiler/internal/project"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/numeric"
)

type checker struct {
	ctx    *project.CompilerContext
	module *project.Module
}

// Concrete references convert to satisfied interface borrows, while owned
// concrete pointers erase by adopting their existing allocation.
const allowImplicitInterfaceConversion = true

// --- helpers -----------------------------------------------------------------

// enclosingFnDecl walks up the scope chain and returns the FnDecl of the
// enclosing function, or nil if not inside a function body.
func (c *checker) enclosingFnDecl(scope *table.Scope) *ast.FnDecl {
	if c == nil || c.module == nil || c.module.ModuleScope == nil {
		return nil
	}
	for s := scope; s != nil && s != c.module.ModuleScope; s = s.Parent() {
		for _, sym := range c.module.ModuleScope.Symbols() {
			if (sym.Kind == symbols.SymbolFunc || sym.Kind == symbols.SymbolMethod) && sym.Scope == s {
				if fn, ok := sym.ASTNode.(*ast.FnDecl); ok {
					return fn
				}
			}
		}
	}
	return nil
}

func (c *checker) requireValueType(expr ast.Expr, typ typeinfo.Type, context string) typeinfo.Type {
	if typ != nil {
		return typ
	}
	if c != nil && c.ctx != nil {
		c.ctx.Diagnostics.Add(invalidExpressionError(expr, context+" requires a value-producing expression"))
	}
	return &typeinfo.InvalidType{}
}

func (c *checker) expandedDefaultBinding(ident *ast.Ident) (place.Binding, bool) {
	if c == nil || c.module == nil || c.module.Semantics == nil || ident == nil {
		return place.Binding{}, false
	}
	if _, ok := c.module.Semantics.ExpandedDefaultBindings[ident.ID()]; !ok {
		return place.Binding{}, false
	}
	return place.Binding{Symbol: c.module.Semantics.ResolvedSymbols[ident.ID()]}, true
}

func (c *checker) isLowerableType(t typeinfo.Type) bool {
	// Semantic indirection may permit recursion, but LLVM type text cannot yet
	// name recursive runtime shells. Re-entering an active type must reject here.
	visiting := make(map[typeinfo.Type]struct{})
	var check func(typeinfo.Type) bool
	check = func(t typeinfo.Type) bool {
		t = typeinfo.Underlying(t)
		if t == nil {
			return false
		}
		if _, found := visiting[t]; found {
			return false
		}
		visiting[t] = struct{}{}
		defer delete(visiting, t)

		switch typ := t.(type) {
		case *typeinfo.IntegerType, *typeinfo.ByteType, *typeinfo.FloatType, *typeinfo.BoolType, *typeinfo.CStrType, *typeinfo.StringType:
			return true
		case *typeinfo.OwnedPtrType:
			target, ok := typeinfo.PointerTarget(typ)
			return ok && target != nil
		case *typeinfo.RawPtrType:
			return typ != nil
		case *typeinfo.RefType:
			if typ == nil || typ.Target == nil {
				return false
			}
			if _, nested := typeinfo.Underlying(typ.Target).(*typeinfo.RefType); nested {
				return false
			}
			if target, ok := typeinfo.Underlying(typ.Target).(*typeinfo.ArrayType); ok && target != nil && target.Len == "" && !target.Dynamic {
				return target.Elem != nil && check(target.Elem)
			}
			return check(typ.Target)
		case *typeinfo.OptionalType:
			return typ != nil && typ.Inner != nil && check(typ.Inner)
		case *typeinfo.ArrayType:
			return typ != nil && (typ.Dynamic || typ.Len != "") && typ.Elem != nil && check(typ.Elem)
		case *typeinfo.StructType:
			if typ == nil {
				return false
			}
			for _, field := range typ.Fields {
				if !check(field.Type) {
					return false
				}
			}
			return true
		case *typeinfo.InterfaceType:
			if typ == nil {
				return false
			}
			for _, method := range typ.Methods {
				if len(method.Params) == 0 {
					return false
				}
				for i, param := range method.Params {
					if i == 0 {
						continue
					}
					if typeinfo.ContainsAbstractSelf(param.Type) || !check(param.Type) {
						return false
					}
				}
				if method.Return != nil && (typeinfo.ContainsAbstractSelf(method.Return) || !check(method.Return)) {
					return false
				}
			}
			return true
		case *typeinfo.FuncType:
			if typ == nil {
				return false
			}
			for _, param := range typ.Params {
				if !check(param) {
					return false
				}
			}
			return typ.Return == nil || check(typ.Return)
		case *typeinfo.EnumType:
			return typ != nil
		default:
			return false
		}
	}
	return check(t)
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

// -----------------------------------------------------------------------------

func (c *checker) checkModule() {
	if c == nil || c.module == nil || c.module.AST == nil {
		return
	}
	c.checkFunctionTypeContracts()
	ast.ForEachDecl(c.module.AST, func(decl ast.Decl) bool {
		c.checkDeclAttributes(decl)
		typeDecl, ok := decl.(ast.TypeDecl)
		if !ok {
			return true
		}
		if iface, ok := typeDecl.(*ast.InterfaceDecl); ok {
			c.checkInterfaceDecl(iface)
		}
		c.checkTypeDeclReferenceStorage(typeDecl)
		return true
	})
	ast.ForEachDecl(c.module.AST, func(decl ast.Decl) bool {
		switch node := decl.(type) {
		case *ast.LetDecl:
			if c.module.ModuleScope != nil {
				c.checkBinding(c.module.ModuleScope, node, false)
			}
		case *ast.ConstDecl:
			if c.module.ModuleScope != nil {
				c.checkBinding(c.module.ModuleScope, node, true)
			}
		}
		return true
	})
	ast.ForEachDecl(c.module.AST, func(decl ast.Decl) bool {
		switch node := decl.(type) {
		case *ast.FnDecl:
			if node == nil {
				return true
			}
			var sym *symbols.Symbol
			if node.Receiver != nil {
				sym = c.module.Semantics.MethodSymbol[node.ID()]
				c.checkReceiverFunction(node)
			} else {
				sym, _ = c.module.ModuleScope.Lookup(node.Name.Name)
			}
			if sym == nil {
				return true
			}
			c.checkFunction(sym, node)
		}
		return true
	})
}

func (c *checker) checkDeclAttributes(decl ast.Decl) {
	attributed, ok := decl.(ast.AttributedNode)
	if !ok || attributed == nil {
		return
	}
	fn, _ := decl.(*ast.FnDecl)
	target := ast.AttributeTarget(0)
	if fn != nil {
		target = ast.AttributeTargetFunc
	} else if _, ok := decl.(ast.TypeDecl); ok {
		target = ast.AttributeTargetType
	}
	attrs := attributed.GetAttributes()
	nameCounts := make(map[string]int, len(attrs))
	for _, attr := range attrs {
		nameCounts[attr.Name]++
	}
	seenNames := make(map[string]ast.Attribute, len(attrs))
	seenGroups := make(map[ast.AttributeConflictGroup]ast.Attribute, len(attrs))
	for _, attr := range attrs {
		def, ok := ast.AttributeDefinitions[attr.Name]
		if !ok {
			c.ctx.Diagnostics.Add(invalidAttributeError(attr,
				fmt.Sprintf("unknown attribute `#[%s]`", attr.Name)))
			continue
		}
		if target == 0 || def.Targets&target == 0 {
			c.ctx.Diagnostics.Add(invalidAttributeError(attr,
				fmt.Sprintf("attribute `#[%s]` cannot be used on this declaration", attr.Name)))
			continue
		}
		requiredArgs := 0
		for _, spec := range def.Args {
			if !spec.Optional {
				requiredArgs++
			}
		}
		if len(attr.Args) < requiredArgs || len(attr.Args) > len(def.Args) {
			c.ctx.Diagnostics.Add(invalidAttributeError(attr,
				fmt.Sprintf("invalid arguments for attribute `#[%s]`", attr.Name)))
			continue
		}
		validArgs := true
		for i, arg := range attr.Args {
			spec := def.Args[i]
			if named, ok := spec.Type.(*ast.NamedType); ok && named != nil && named.Name == "cstr" {
				if _, ok := arg.(*ast.StringLit); !ok {
					validArgs = false
					break
				}
				continue
			}
			if named, ok := spec.Type.(*ast.NamedType); ok && named != nil && named.Name == "i32" {
				if _, ok := arg.(*ast.NumberLit); !ok {
					validArgs = false
					break
				}
			}
			expectedType := typeinfo.TypeFromSyntax(spec.Type, typeinfo.SyntaxOptions{AllowAbstractSelf: true})
			argType := c.typeExpr(c.module.ModuleScope, arg, expectedType)
			if typeinfo.IsInvalidOrUnknown(argType) {
				validArgs = false
				break
			}
			if !typeinfo.SameType(argType, expectedType) &&
				!c.assignable(expectedType, argType) &&
				!c.assignable(argType, expectedType) {
				validArgs = false
				break
			}
		}
		if !validArgs {
			c.ctx.Diagnostics.Add(invalidAttributeError(attr,
				fmt.Sprintf("invalid arguments for attribute `#[%s]`", attr.Name)))
			continue
		}
		if prev, ok := seenNames[attr.Name]; ok {
			d := invalidAttributeError(attr,
				fmt.Sprintf("duplicate attribute `#[%s]`", prev.Name))
			d.WithSecondaryLabel(prev.Location, "previous attribute here")
			c.ctx.Diagnostics.Add(d)
			continue
		}
		seenNames[attr.Name] = attr
		switch attr.Name {
		case ast.AttributeTargetOS:
			if nameCounts[attr.Name] == 1 {
				c.ctx.Diagnostics.AddWarning(
					diagnostics.WarnIgnoredTargetOS,
					"attribute `#[target_os]` is reserved for future target-specific declaration filtering and is currently ignored",
					attr.Location,
					"",
				)
			}
		case ast.AttributeExtern:
			if fn != nil && fn.Body != nil {
				d := invalidAttributeError(attr, "attribute `#[extern]` requires a body-less function declaration")
				d.WithHelp("remove body to declare extern function")
				d.WithHelp("remove `#[extern]` to keep local definition")
				c.ctx.Diagnostics.Add(d)
			}
		}
		if def.ConflictGroup == ast.AttributeConflictNone {
			continue
		}
		if prev, ok := seenGroups[def.ConflictGroup]; ok {
			d := invalidAttributeError(attr,
				fmt.Sprintf("conflicting attributes `#[%s]` and `#[%s]`", prev.Name, attr.Name))
			d.WithSecondaryLabel(prev.Location, "conflicting attribute here")
			c.ctx.Diagnostics.Add(d)
			continue
		}
		seenGroups[def.ConflictGroup] = attr
	}
}

func (c *checker) checkFunction(sym *symbols.Symbol, fn *ast.FnDecl) {
	if c == nil || sym == nil || fn == nil {
		return
	}
	c.checkFunctionShape(fn)
	if sym.Scope == nil {
		return
	}
	funcScope := sym.Scope.(*table.Scope)
	for _, param := range fn.ParamsWithReceiver() {
		if param.Name == nil {
			continue
		}
		paramSym, ok := funcScope.LookupNode(param.Name)
		if !ok || paramSym == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "missing parameter binding", ast.LocOf(param.Name), "")
			return
		}
		paramSym.BindType(typeinfo.TypeFromSyntax(param.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false)))
	}
	c.checkDefaultParameters(funcScope, fn)
	if fn.Body == nil {
		return
	}
	returnType := typeinfo.TypeFromSyntax(fn.ReturnType, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
	c.checkBlock(funcScope, fn.Body, returnType)
}

func (c *checker) checkDefaultParameters(scope *table.Scope, fn *ast.FnDecl) {
	if c == nil || scope == nil || fn == nil {
		return
	}
	params := fn.ParamsWithReceiver()
	seenDefault := false
	for i, param := range params {
		if param.Default == nil {
			if seenDefault {
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidDeclaration,
					"required parameter cannot follow parameter with default", ast.LocOf(param.Name), "")
			}
			continue
		}
		seenDefault = true
		if i == 0 && fn.Receiver != nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidDeclaration,
				"receiver cannot have a default value", ast.LocOf(param.Default), "")
		}
		paramType := typeinfo.TypeFromSyntax(param.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
		if paramType == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"defaulted parameter requires an explicit type", ast.LocOf(param.Name), "")
			continue
		}
		defaultType := c.typeExpr(scope, param.Default, paramType)
		defaultType = c.requireValueType(param.Default, defaultType, "default value")
		if !typeinfo.IsInvalidOrUnknown(defaultType) && !c.assignable(paramType, defaultType) {
			c.ctx.Diagnostics.Add(typeMismatchError(param.Default,
				fmt.Sprintf("cannot implicitly convert %s to %s", typeinfo.TypeText(defaultType), typeinfo.TypeText(paramType))))
		}
		c.rejectOwnedParameterReferences(scope, fn, i, param.Default)
	}
}

func (c *checker) rejectOwnedParameterReferences(scope *table.Scope, fn *ast.FnDecl, current int, expr ast.Expr) {
	if c == nil || c.module == nil || c.module.Semantics == nil || fn == nil || expr == nil {
		return
	}
	params := fn.ParamsWithReceiver()
	paramIndexes := make(map[*symbols.Symbol]int, len(params))
	for i, param := range params {
		if param.Name == nil {
			continue
		}
		if sym, ok := scope.Lookup(param.Name.Name); ok && sym != nil {
			paramIndexes[sym] = i
		}
	}
	ast.Inspect(expr, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok || ident == nil {
			return true
		}
		sym := c.module.Semantics.ResolvedSymbols[ident.ID()]
		index, isParam := paramIndexes[sym]
		if !isParam || index >= current || index < 0 || index >= len(params) {
			return true
		}
		paramType := typeinfo.TypeFromSyntax(params[index].Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
		if typeinfo.IsImplicitCopyType(paramType) {
			return true
		}
		if _, _, reference := typeinfo.ReferenceValueTarget(paramType); reference {
			return true
		}
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCopy,
			"default value cannot reuse move-only parameter; bind or pass an owned value explicitly", ast.LocOf(ident), "")
		return true
	})
}

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
		if !c.assignable(returnType, retType) {
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
	default:
		return // resolver already diagnosed unsupported statements
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
	if !c.assignable(targetType, valueType) {
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
	if declType != nil && !c.assignable(declType, valType) {
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

func (c *checker) checkFunctionShape(decl *ast.FnDecl) {
	if decl == nil {
		return
	}
	opts := project.TypeSyntaxOptions(c.ctx, c.module, nil, false)
	fnType := typeinfo.FuncTypeFromDeclWithOptions(decl, opts)
	if !c.checkCallableReturn(decl.ReturnType, decl, fnType, decl.ReturnOrigins) {
		return
	}
	for _, param := range decl.ParamsWithReceiver() {
		paramType := typeinfo.TypeFromSyntax(param.Type, opts)
		if c.rejectUnsizedType(paramType, param.Type, "parameter") {
			return
		}
		if c.rejectReferenceStorage(paramType, param.Type, "parameter aggregate types", false) {
			return
		}
		if !c.isLowerableType(paramType) {
			site := ast.Node(decl)
			if param.Name != nil {
				site = param.Name
			}
			c.ctx.Diagnostics.Add(invalidTypeError(site,
				"parameter type is not lowerable in current compiler stage"))
			return
		}
	}
}

func (c *checker) checkCallableReturn(typeNode ast.TypeExpr, fallback ast.Node, fnType *typeinfo.FuncType, clause *ast.ReturnOriginClause) bool {
	if fnType == nil {
		return false
	}
	typ := fnType.Return
	site := ast.LocOf(typeNode)
	if site == nil {
		site = ast.LocOf(fallback)
	}
	_, returnMutable, referenceReturn := typeinfo.ReferenceValueTarget(typ)
	if typ == nil {
		if clause != nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
				"`from` clause requires a reference return type", clause.Location, "")
			return false
		}
		return true
	}
	if typeinfo.ContainsReference(typ) && !referenceReturn {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
			"reference return must be a direct reference or optional reference value", site, "")
		return false
	}
	if !referenceReturn {
		if clause != nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
				"`from` clause is only valid on reference returns", clause.Location, "")
			return false
		}
	} else if clause == nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
			"reference return requires a `from` clause naming borrowed source parameters", site, "")
		return false
	} else if fnType.ReturnOrigins == nil || len(fnType.ReturnOrigins.Sources) != len(clause.Sources) {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
			"invalid reference return origin contract", clause.Location, "")
		return false
	} else {
		valid := len(clause.Sources) > 0
		if !valid {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
				"`from` clause must name at least one borrowed source parameter", clause.Location, "")
		}
		seen := make(map[int]struct{}, len(clause.Sources))
		for i, source := range clause.Sources {
			sourceSite := clause.Location
			if source != nil {
				sourceSite = source.Location
			}
			slot := fnType.ReturnOrigins.Sources[i]
			if slot < 0 || slot >= len(fnType.Params) {
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
					"`from` source must name a borrowed parameter or `self` receiver", sourceSite, "")
				valid = false
				continue
			}
			if _, duplicate := seen[slot]; duplicate {
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
					"duplicate source in reference return `from` clause", sourceSite, "")
				valid = false
				continue
			}
			seen[slot] = struct{}{}
			_, sourceMutable, borrowed := typeinfo.ReferenceValueTarget(fnType.Params[slot])
			if !borrowed {
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
					"reference return source must be a borrowed parameter", sourceSite, "")
				valid = false
				continue
			}
			if returnMutable && !sourceMutable {
				c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
					"mutable reference return requires mutable borrowed sources", sourceSite, "")
				valid = false
			}
		}
		if !valid {
			return false
		}
	}
	if c.rejectUnsizedType(typ, typeNode, "function return") {
		return false
	}
	if !c.isLowerableType(typ) {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
			"function return type is not lowerable in current compiler stage", site, "")
		return false
	}
	return true
}

func (c *checker) checkFunctionTypeContracts() {
	opts := project.TypeSyntaxOptions(c.ctx, c.module, nil, false)
	ast.ForEachDecl(c.module.AST, func(decl ast.Decl) bool {
		ast.Inspect(decl, func(node ast.Node) bool {
			fnTypeSyntax, ok := node.(*ast.FuncType)
			if !ok || fnTypeSyntax == nil {
				return true
			}
			fnType, _ := typeinfo.TypeFromSyntax(fnTypeSyntax, opts).(*typeinfo.FuncType)
			c.checkCallableReturn(fnTypeSyntax.Return, fnTypeSyntax, fnType, fnTypeSyntax.ReturnOrigins)
			return true
		})
		return true
	})
}

func (c *checker) checkTypeDeclReferenceStorage(decl ast.TypeDecl) {
	if decl == nil {
		return
	}
	opts := project.TypeSyntaxOptions(c.ctx, c.module, nil, false)
	switch node := decl.(type) {
	case *ast.StructDecl:
		strct, ok := node.Type.(*ast.StructType)
		if !ok || strct == nil {
			return
		}
		for _, field := range strct.Fields {
			fieldType := typeinfo.TypeFromSyntax(field.Type, opts)
			c.rejectReferenceStorage(fieldType, field.Type, "struct fields", true)
			c.rejectUnsizedType(fieldType, field.Type, "struct field")
		}
	case *ast.TypeAliasDecl:
		typ := typeinfo.TypeFromSyntax(node.Type, opts)
		c.rejectReferenceStorage(typ, node.Type, "array or heap-owned type aliases", false)
	}
}

func (c *checker) checkInterfaceDecl(decl *ast.InterfaceDecl) {
	if c == nil || decl == nil {
		return
	}
	// Interface declarations store canonical payload in Type so anonymous and
	// named interface syntax share one method shape through the pipeline.
	iface, ok := decl.Type.(*ast.InterfaceType)
	if !ok || iface == nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidTypeInParser, "interface declaration missing interface payload", ast.LocOf(decl), "")
		return
	}
	resolvedIface, _ := typeinfo.TypeFromSyntax(iface, project.TypeSyntaxOptions(c.ctx, c.module, nil, false)).(*typeinfo.InterfaceType)
	for methodIndex, method := range iface.Methods {
		if method.Name == nil || method.Name.Name == "" {
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "interface method name required", method.Location, "")
			continue
		}
		receiverOpts := project.TypeSyntaxOptions(c.ctx, c.module, nil, true)
		if method.Receiver == nil {
			c.ctx.Diagnostics.Add(invalidTypeError(method.Name,
				"iface methods require Self, &Self, or &mut Self receiver"))
			continue
		}
		receiverType := typeinfo.TypeFromSyntax(method.Receiver.Type, receiverOpts)
		receiverTarget, ok := typeinfo.ReceiverTarget(receiverType)
		receiverSelf, abstractSelf := receiverTarget.(*typeinfo.NamedType)
		_, ownedReceiver := typeinfo.PointerTarget(receiverType)
		if !ok || ownedReceiver || !abstractSelf || receiverSelf == nil || receiverSelf.Name != "Self" {
			c.ctx.Diagnostics.Add(invalidTypeError(method.Receiver.Type,
				"iface method receiver must be Self, &Self, or &mut Self"))
		}
		opts := project.TypeSyntaxOptions(c.ctx, c.module, nil, false)
		for _, param := range method.Params {
			paramType := typeinfo.TypeFromSyntax(param.Type, opts)
			if c.rejectUnsizedType(paramType, param.Type, "interface method parameter") {
				continue
			}
			if c.rejectReferenceStorage(paramType, param.Type, "interface parameter aggregate types", false) {
				continue
			}
			if paramType != nil && !c.isLowerableType(paramType) {
				site := ast.Node(decl)
				if param.Name != nil {
					site = param.Name
				}
				c.ctx.Diagnostics.Add(invalidTypeError(site,
					"interface method parameter type is not lowerable in current compiler stage"))
			}
		}
		if resolvedIface != nil && methodIndex < len(resolvedIface.Methods) {
			c.checkCallableReturn(method.ReturnType, decl, resolvedIface.Methods[methodIndex].CallableType(), method.ReturnOrigins)
		}
	}

}

func (c *checker) checkReceiverFunction(fn *ast.FnDecl) {
	if c == nil || c.module == nil || fn == nil || fn.Receiver == nil {
		return
	}
	if fn.Receiver.Name == nil || fn.Receiver.Name.Name == "" {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidMethodReceiver,
			"receiver function requires a named receiver", ast.LocOf(fn.Receiver.Type), "")
		return
	}
	receiverType := typeinfo.TypeFromSyntax(fn.Receiver.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
	targetType, ok := typeinfo.ReceiverTarget(receiverType)
	defined, named := targetType.(*typeinfo.DefinedType)
	if !ok || !named || defined == nil || !isValidReceiverType(receiverType, defined) {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidMethodReceiver,
			"receiver target must be a concrete named type declared in current module", ast.LocOf(fn.Receiver.Type), "")
		return
	}
	sym, local := c.module.ModuleScope.LookupLocal(defined.Name)
	if !local || sym == nil || !typeinfo.SameType(sym.Type, defined) {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidMethodReceiver,
			"receiver target must be declared in current module", ast.LocOf(fn.Receiver.Type), "")
		return
	}
	switch sym.ASTNode.(type) {
	case *ast.StructDecl, *ast.EnumDecl:
		return
	default:
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidMethodReceiver,
			"receiver target must be a concrete named type", ast.LocOf(fn.Receiver.Type), "")
	}
}

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

// addInterfaceHint adds a help message showing missing interface methods when
// the destination type is an interface and the source doesn't satisfy it.
func (c *checker) addInterfaceHint(d *diagnostics.Diagnostic, dst, src typeinfo.Type) {
	iface, ok := typeinfo.InterfaceTypeOf(dst)
	if !ok {
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

// typeExpr computes the type of an expression using scope lookup, records it in the
// module's ExprTypes side table for downstream phases, and returns it.
func (c *checker) typeExpr(scope *table.Scope, expr ast.Expr, expected typeinfo.Type) (resolved typeinfo.Type) {
	if expr == nil {
		return nil
	}
	defer func() {
		if resolved != nil {
			if c.module != nil && c.module.Semantics != nil {
				c.module.Semantics.ExprTypes[expr.ID()] = resolved
			}
		}
	}()
	switch node := expr.(type) {
	case *ast.NumberLit:
		return c.typeNumber(node, expected)

	case *ast.StringLit:
		return &typeinfo.CStrType{}

	case *ast.BoolLit:
		return &typeinfo.BoolType{}

	case *ast.NoneLit:
		if optional, ok := typeinfo.Underlying(expected).(*typeinfo.OptionalType); ok && optional != nil {
			return expected
		}
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
			"`none` requires optional context", ast.LocOf(node), "")
		return &typeinfo.InvalidType{}

	case *ast.AddressExpr:
		return c.typeAddressExpr(scope, node, expected)

	case *ast.Ident:
		var sym *symbols.Symbol
		var ok bool
		if c.module != nil && c.module.Semantics != nil {
			sym = c.module.Semantics.ResolvedSymbols[node.ID()]
			ok = sym != nil
		}
		if !ok {
			sym, ok = scope.Lookup(node.Name)
		}
		if !ok || sym == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrUnknownIdentifier,
				fmt.Sprintf("unknown identifier `%s`\n", node.Name), ast.LocOf(node), "")
			return &typeinfo.InvalidType{}
		}
		if sym.Initializing || (!sym.Initialized && symbols.RequiresInitialization(sym.Kind)) {
			return &typeinfo.InvalidType{}
		}
		if sym.CompilerOp != "" {
			c.ctx.Diagnostics.Add(invalidExpressionError(node,
				fmt.Sprintf("compiler operation `%s` must be called directly", node.Name)))
			return &typeinfo.InvalidType{}
		}
		t, ok := symbols.GetSymbolType(sym)
		if !ok || t == nil {
			return &typeinfo.UnknownType{}
		}
		return t

	case *ast.ScopeResolution:
		return c.qualifiedScopeType(node)

	case *ast.SelectorExpr:
		return c.typeSelectorExpr(scope, node)

	case *ast.IndexExpr:
		return c.typeIndexExpr(scope, node)

	case *ast.RangeExpr:
		c.ctx.Diagnostics.Add(invalidExpressionError(node,
			"range expression is only valid inside an index expression"))
		return &typeinfo.InvalidType{}

	case *ast.StructLit:
		return c.typeStructLit(scope, node, expected)

	case *ast.ArrayLit:
		return c.typeArrayLit(scope, node)

	case *ast.UnaryExpr:
		return c.typeUnaryExpr(scope, node, expected)

	case *ast.BinaryExpr:
		return c.typeBinaryExpr(scope, node, expected)

	case *ast.CallExpr:
		return c.typeCallExpr(scope, node, expected)

	case *ast.FreeExpr:
		return c.typeFreeExpr(scope, node)

	case *ast.PrintExpr:
		return c.typePrintExpr(scope, node)

	case *ast.AsExpr:
		return c.typeAsExpr(scope, node)

	default:
		return nil // resolver already diagnosed unsupported expressions
	}
}

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

func (c *checker) typeUnaryExpr(scope *table.Scope, node *ast.UnaryExpr, expected typeinfo.Type) typeinfo.Type {
	if node.Op != "+" && node.Op != "-" && node.Op != "!" && node.Op != "~" {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported unary operator `"+node.Op+"`"))
		return nil
	}

	argExpected := typeinfo.Type(nil)
	if node.Op != "!" && expected != nil &&
		((node.Op == "~" && typeinfo.IsIntegral(expected)) || (node.Op != "~" && typeinfo.IsArithmetic(expected))) {
		argExpected = expected
		if node.Op == "-" {
			if _, ok := expected.(*typeinfo.IntegerType); ok {
				if _, ok := node.Expr.(*ast.NumberLit); ok {
					argExpected = nil
				}
			}
		}
	}

	argType := c.typeExpr(scope, node.Expr, argExpected)
	argType = c.requireValueType(node.Expr, argType, "unary operand")
	if typeinfo.IsInvalidOrUnknown(argType) {
		return &typeinfo.InvalidType{}
	}
	if node.Op == "!" {
		if !typeinfo.SameType(argType, &typeinfo.BoolType{}) {
			c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node.Expr, "`!` operand must be bool"))
			return nil
		}
		return &typeinfo.BoolType{}
	}
	if node.Op == "~" {
		if !typeinfo.IsIntegral(argType) {
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"unsupported unary operand type for operator `~`"))
			return nil
		}
		return argType
	}
	if !typeinfo.IsArithmetic(argType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported unary operand type"))
		return nil
	}
	return argType
}

func (c *checker) typeAddressExpr(scope *table.Scope, node *ast.AddressExpr, expected typeinfo.Type) typeinfo.Type {
	if node == nil || node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	valueType := c.typeExpr(scope, node.Expr, nil)
	valueType = c.requireValueType(node.Expr, valueType, "address operand")
	if typeinfo.IsInvalidOrUnknown(valueType) {
		return &typeinfo.InvalidType{}
	}
	exprType := func(expr ast.Expr) typeinfo.Type {
		return c.typeExpr(scope, expr, nil)
	}
	addressable := place.Addressable(scope, node.Expr, exprType, c.expandedDefaultBinding)
	if node.Mode == ast.AddressMutable {
		if mutable, sharedReference := place.MutableAddressable(scope, node.Expr, exprType, c.expandedDefaultBinding); addressable && !mutable {
			diagnostic := c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
				"mutable reference requires mutable addressable storage", ast.LocOf(node.Expr), "")
			if sharedReference != nil {
				diagnostic.WithSecondaryLabel(ast.LocOf(node.Expr), "value is behind an immutable reference")
			}
			return &typeinfo.InvalidType{}
		}
		if _, _, nested := typeinfo.ReferenceTarget(typeinfo.Underlying(valueType)); nested {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"reference-to-reference types are not supported in v1", ast.LocOf(node), "")
			return &typeinfo.InvalidType{}
		}
		return &typeinfo.RefType{Mutable: true, Target: valueType}
	}
	if node.Mode == ast.AddressRaw && !addressable {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidExpression,
			"address operator requires addressable storage", ast.LocOf(node.Expr), "")
		return &typeinfo.InvalidType{}
	}
	if node.Mode == ast.AddressShared {
		if _, _, nested := typeinfo.ReferenceTarget(typeinfo.Underlying(valueType)); nested {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"reference-to-reference types are not supported in v1", ast.LocOf(node), "")
			return &typeinfo.InvalidType{}
		}
		return &typeinfo.RefType{Target: valueType}
	}
	return &typeinfo.RawPtrType{}
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

func (c *checker) typeBinaryExpr(scope *table.Scope, node *ast.BinaryExpr, expected typeinfo.Type) typeinfo.Type {
	if !c.allowedOp(node.Op) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported binary operator `"+node.Op+"`"))
		return nil
	}

	operandExpected := expected
	if binaryResultIsBool(node.Op) {
		operandExpected = nil
	}

	var left, right typeinfo.Type
	if isNoneExpr(node.Left) && !isNoneExpr(node.Right) {
		right = c.typeExpr(scope, node.Right, operandExpected)
		left = c.typeExpr(scope, node.Left, optionalOperandExpected(right))
	} else if leftNumber, leftLiteral := node.Left.(*ast.NumberLit); leftLiteral {
		if rightNumber, rightLiteral := node.Right.(*ast.NumberLit); !rightLiteral {
			right = c.typeExpr(scope, node.Right, operandExpected)
			left = c.typeExpr(scope, node.Left, right)
		} else if leftNumber.ExplicitType != "" && rightNumber.ExplicitType == "" {
			left = c.typeExpr(scope, node.Left, operandExpected)
			right = c.typeExpr(scope, node.Right, left)
		} else if leftNumber.ExplicitType == "" && rightNumber.ExplicitType != "" {
			right = c.typeExpr(scope, node.Right, operandExpected)
			left = c.typeExpr(scope, node.Left, right)
		} else {
			left = c.typeExpr(scope, node.Left, operandExpected)
			right = c.typeExpr(scope, node.Right, operandExpected)
		}
	} else if _, rightLiteral := node.Right.(*ast.NumberLit); rightLiteral {
		left = c.typeExpr(scope, node.Left, operandExpected)
		right = c.typeExpr(scope, node.Right, left)
	} else {
		left = c.typeExpr(scope, node.Left, operandExpected)
		rightExpected := operandExpected
		if isNoneExpr(node.Right) {
			rightExpected = optionalOperandExpected(left)
		}
		right = c.typeExpr(scope, node.Right, rightExpected)
	}
	left = c.requireValueType(node.Left, left, "left operand")
	right = c.requireValueType(node.Right, right, "right operand")

	if typeinfo.IsInvalidOrUnknown(left) || typeinfo.IsInvalidOrUnknown(right) {
		return &typeinfo.InvalidType{}
	}
	if (node.Op == "==" || node.Op == "!=") && (isOptionalType(left) || isOptionalType(right)) &&
		!isNoneExpr(node.Left) && !isNoneExpr(node.Right) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"optional equality currently requires `none` on one side"))
		return &typeinfo.InvalidType{}
	}

	commonType := typeinfo.CommonNumericType(left, right)
	if commonType == nil && !c.assignable(left, right) && !c.assignable(right, left) {
		c.ctx.Diagnostics.Add(typeMismatchError(node,
			fmt.Sprintf("operand types mismatch: %s vs %s",
				typeinfo.TypeText(left), typeinfo.TypeText(right))))
		return &typeinfo.InvalidType{}
	}

	exprType := left
	if commonType != nil {
		exprType = commonType
	}
	switch node.Op {
	case "&&", "||":
		if !typeinfo.SameType(left, &typeinfo.BoolType{}) || !typeinfo.SameType(right, &typeinfo.BoolType{}) {
			c.ctx.Diagnostics.Add(explicitBoolCastRequiredError(node, "logical operators require bool operands"))
			return nil
		}
		return &typeinfo.BoolType{}
	case "==", "!=", "<", "<=", ">", ">=":
		_, leftShape, leftSequence := indexableSequence(left)
		_, rightShape, rightSequence := indexableSequence(right)
		leftSliceView := leftSequence && (leftShape == indexableSharedSliceView || leftShape == indexableMutableSliceView)
		rightSliceView := rightSequence && (rightShape == indexableSharedSliceView || rightShape == indexableMutableSliceView)
		if leftSliceView || rightSliceView {
			c.ctx.Diagnostics.Add(invalidOperationError(node,
				"slice-view comparison is not supported in current compiler stage"))
			return &typeinfo.InvalidType{}
		}
		return &typeinfo.BoolType{}
	}

	if !c.validBinaryTypes(node.Op, exprType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported operand type for operator `"+node.Op+"`"))
		return nil
	}
	if node.Op == "<<" || node.Op == ">>" {
		if value, ok := consteval.EvaluateExpr(c.ctx, c.module, scope, node.Right, exprType); ok {
			if count, ok := value.(*constvalue.IntConst); ok && count != nil {
				parsed, err := numeric.StringToBigInt(count.Value)
				_, bits, _ := typeinfo.NumericInfo(exprType)
				if err == nil {
					normalized, normalizedOK := constvalue.NormalizeInteger(parsed,
						typeinfo.TypeText(typeinfo.Underlying(exprType)))
					if normalizedOK && (normalized.Sign() < 0 || normalized.Cmp(big.NewInt(int64(bits))) >= 0) {
						c.ctx.Diagnostics.Add(invalidOperationError(node.Right,
							fmt.Sprintf("shift count must be between 0 and %d", bits-1)))
						return &typeinfo.InvalidType{}
					}
				}
			}
		}
	}
	return exprType
}

func binaryResultIsBool(op string) bool {
	switch op {
	case "&&", "||", "==", "!=", "<", "<=", ">", ">=":
		return true
	default:
		return false
	}
}

func isNoneExpr(expr ast.Expr) bool {
	_, ok := expr.(*ast.NoneLit)
	return ok
}

func optionalOperandExpected(typ typeinfo.Type) typeinfo.Type {
	if optional, ok := typeinfo.Underlying(typ).(*typeinfo.OptionalType); ok && optional != nil {
		return typ
	}
	return nil
}

func isOptionalType(typ typeinfo.Type) bool {
	_, ok := typeinfo.Underlying(typ).(*typeinfo.OptionalType)
	return ok
}

func (c *checker) typeCallExpr(scope *table.Scope, node *ast.CallExpr, expected typeinfo.Type) typeinfo.Type {
	if selector, ok := node.Callee.(*ast.SelectorExpr); ok && selector != nil {
		return c.typeSelectorCall(scope, selector, node)
	}
	if ident, ok := node.Callee.(*ast.Ident); ok && ident != nil {
		if sym := c.module.Semantics.ResolvedSymbols[ident.ID()]; sym != nil && sym.CompilerOp != "" {
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

	sizeType, ok := typeinfo.NumericTypeFromName("usize")
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

func (c *checker) typeSelectorExpr(scope *table.Scope, node *ast.SelectorExpr) typeinfo.Type {
	if node == nil || node.Expr == nil || node.Name == nil {
		return &typeinfo.InvalidType{}
	}
	baseType := c.typeExpr(scope, node.Expr, nil)
	if baseType == nil || typeinfo.IsInvalidOrUnknown(baseType) {
		return &typeinfo.InvalidType{}
	}
	if field, _, ok := typeinfo.LookupStructField(baseType, node.Name.Name); ok {
		return field.Type
	}
	if methodType, _, ok := c.lookupMethodType(baseType, node.Name.Name); ok {
		return methodType
	}
	d := diagnostics.NewError(fmt.Sprintf("unknown member `%s`", node.Name.Name)).
		WithCode(diagnostics.ErrFieldNotFound).
		WithPrimaryLabel(ast.LocOf(node.Name), "")
	if match, ok := diagnostics.NearestName(node.Name.Name, append(availableFields(baseType), c.availableMethods(baseType)...)); ok {
		d.WithHelp("did you mean `" + match + "`?")
	}
	c.ctx.Diagnostics.Add(d)
	return &typeinfo.InvalidType{}
}

func (c *checker) typeIndexExpr(scope *table.Scope, node *ast.IndexExpr) typeinfo.Type {
	if node == nil || node.Expr == nil || node.Index == nil {
		return &typeinfo.InvalidType{}
	}
	baseType := c.typeExpr(scope, node.Expr, nil)
	if typeinfo.IsInvalidOrUnknown(baseType) {
		return &typeinfo.InvalidType{}
	}
	if rangeIndex, ok := node.Index.(*ast.RangeExpr); ok {
		return c.typeRangeIndexExpr(scope, node, rangeIndex, baseType)
	}
	indexType := c.typeExpr(scope, node.Index, typeinfo.DefaultIntegerType())
	indexType = c.requireValueType(node.Index, indexType, "index")
	if typeinfo.IsInvalidOrUnknown(indexType) {
		return &typeinfo.InvalidType{}
	}
	if !typeinfo.IsIntegral(indexType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node.Index,
			"index expression must be an integer"))
		return &typeinfo.InvalidType{}
	}
	elem, shape, ok := indexableSequence(baseType)
	if !ok {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
			"indexing requires array or slice value"))
		return &typeinfo.InvalidType{}
	}
	if shape != indexableFixedArray {
		return elem
	}
	array := typeinfo.Underlying(baseType).(*typeinfo.ArrayType)
	value, ok := consteval.EvaluateExpr(c.ctx, c.module, scope, node.Index, typeinfo.DefaultIntegerType())
	if !ok {
		return elem
	}
	indexConst, ok := value.(*constvalue.IntConst)
	if !ok || indexConst == nil {
		return elem
	}
	length, lengthErr := strconv.Atoi(array.Len)
	indexValue, indexErr := strconv.Atoi(indexConst.Value)
	if lengthErr == nil && (indexErr != nil || indexValue < 0 || indexValue >= length) {
		c.ctx.Diagnostics.Add(problems.ArrayIndexOutOfBounds(indexConst.Value, array.Len, ast.LocOf(node.Index)))
	}
	return elem
}

func (c *checker) typeRangeIndexExpr(scope *table.Scope, node *ast.IndexExpr, rangeIndex *ast.RangeExpr, baseType typeinfo.Type) typeinfo.Type {
	if c == nil || node == nil || rangeIndex == nil {
		return &typeinfo.InvalidType{}
	}
	elem, shape, ok := indexableSequence(baseType)
	if !ok {
		c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
			"slicing requires array or slice value"))
		return &typeinfo.InvalidType{}
	}
	c.checkRangeBound(scope, rangeIndex.Start)
	c.checkRangeBound(scope, rangeIndex.End)
	exprType := func(expr ast.Expr) typeinfo.Type {
		return c.typeExpr(scope, expr, nil)
	}
	if shape == indexableFixedArray || shape == indexableDynamicArray {
		if !place.Addressable(scope, node.Expr, exprType, c.expandedDefaultBinding) {
			c.ctx.Diagnostics.Add(invalidExpressionError(node.Expr,
				"slicing requires addressable array storage"))
			return &typeinfo.InvalidType{}
		}
	}
	mutable := shape == indexableMutableSliceView
	if shape == indexableFixedArray || shape == indexableDynamicArray {
		mutable, _ = place.MutableAddressable(scope, node.Expr, exprType, c.expandedDefaultBinding)
	}
	return &typeinfo.RefType{
		Mutable: mutable,
		Target:  &typeinfo.ArrayType{Dynamic: true, Elem: elem},
	}
}

type indexableSequenceShape uint8

const (
	indexableFixedArray indexableSequenceShape = iota
	indexableDynamicArray
	indexableSharedSliceView
	indexableMutableSliceView
)

func indexableSequence(t typeinfo.Type) (typeinfo.Type, indexableSequenceShape, bool) {
	switch base := typeinfo.Underlying(t).(type) {
	case *typeinfo.ArrayType:
		if base == nil || base.Elem == nil {
			return nil, 0, false
		}
		if base.Dynamic {
			return base.Elem, indexableDynamicArray, true
		}
		if base.Len != "" {
			return base.Elem, indexableFixedArray, true
		}
	case *typeinfo.RefType:
		if base == nil || base.Target == nil {
			return nil, 0, false
		}
		target, ok := typeinfo.Underlying(base.Target).(*typeinfo.ArrayType)
		if !ok || target == nil || !target.Dynamic || target.Elem == nil {
			return nil, 0, false
		}
		if base.Mutable {
			return target.Elem, indexableMutableSliceView, true
		}
		return target.Elem, indexableSharedSliceView, true
	}
	return nil, 0, false
}

func (c *checker) checkRangeBound(scope *table.Scope, expr ast.Expr) {
	if c == nil || expr == nil {
		return
	}
	boundType := c.typeExpr(scope, expr, typeinfo.DefaultIntegerType())
	boundType = c.requireValueType(expr, boundType, "range bound")
	if typeinfo.IsInvalidOrUnknown(boundType) {
		return
	}
	if !typeinfo.IsIntegral(boundType) {
		c.ctx.Diagnostics.Add(invalidOperationError(expr,
			"range bound must be an integer"))
	}
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

func (c *checker) typeStructLit(scope *table.Scope, node *ast.StructLit, expected typeinfo.Type) typeinfo.Type {
	if node == nil {
		return &typeinfo.InvalidType{}
	}
	if node.Type != nil {
		targetType := typeinfo.TypeFromSyntax(node.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
		targetStruct, ok := typeinfo.Underlying(targetType).(*typeinfo.StructType)
		if !ok || targetStruct == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"composite literal type must be struct", ast.LocOf(node.Type), "")
			return &typeinfo.InvalidType{}
		}
		return c.typeStructLitWithExpected(scope, node, targetStruct, targetType)
	}
	targetStruct, targetType := c.expectedStructType(expected)
	if targetStruct != nil {
		return c.typeStructLitWithExpected(scope, node, targetStruct, targetType)
	}
	return c.typeStructLitAnonymous(scope, node)
}

func (c *checker) expectedStructType(expected typeinfo.Type) (*typeinfo.StructType, typeinfo.Type) {
	if expected == nil {
		return nil, nil
	}
	if strct, ok := typeinfo.Underlying(expected).(*typeinfo.StructType); ok && strct != nil {
		return strct, expected
	}
	return nil, nil
}

func (c *checker) typeStructLitWithExpected(scope *table.Scope, node *ast.StructLit, targetStruct *typeinfo.StructType, targetType typeinfo.Type) typeinfo.Type {
	if targetStruct == nil {
		return &typeinfo.InvalidType{}
	}
	fieldsByName := make(map[string]ast.StructLitField, len(node.Fields))
	for _, field := range node.Fields {
		if field.Name == nil || field.Name.Name == "" {
			continue
		}
		if _, exists := fieldsByName[field.Name.Name]; exists {
			c.ctx.Diagnostics.AddError(diagnostics.ErrDuplicateField,
				"duplicate struct literal field `"+field.Name.Name+"`", ast.LocOf(field.Name), "")
			continue
		}
		fieldsByName[field.Name.Name] = field
	}
	for _, targetField := range targetStruct.Fields {
		field, ok := fieldsByName[targetField.Name]
		if !ok {
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingInitializer,
				"missing struct literal field `"+targetField.Name+"`", ast.LocOf(node), "").
				WithHelp(fmt.Sprintf("required fields: %s", strings.Join(availableFields(targetType), ", ")))
			continue
		}
		valueType := c.typeExpr(scope, field.Value, targetField.Type)
		valueType = c.requireValueType(field.Value, valueType, "struct field initializer")
		if typeinfo.IsInvalidOrUnknown(valueType) {
			continue
		}
		if !c.assignable(targetField.Type, valueType) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot assign %s to field `%s` of type %s",
					typeinfo.TypeText(valueType), targetField.Name, typeinfo.TypeText(targetField.Type)), ast.LocOf(field.Value), "")
		}
		delete(fieldsByName, targetField.Name)
	}
	for name, field := range fieldsByName {
		c.ctx.Diagnostics.AddError(diagnostics.ErrFieldNotFound,
			"unknown struct literal field `"+name+"`", ast.LocOf(field.Name), "")
	}
	return targetType
}

func (c *checker) typeStructLitAnonymous(scope *table.Scope, node *ast.StructLit) typeinfo.Type {
	fields := make([]typeinfo.Field, 0, len(node.Fields))
	seen := make(map[string]struct{}, len(node.Fields))
	for _, field := range node.Fields {
		if field.Name == nil || field.Name.Name == "" {
			continue
		}
		if _, exists := seen[field.Name.Name]; exists {
			c.ctx.Diagnostics.AddError(diagnostics.ErrDuplicateField,
				"duplicate struct literal field `"+field.Name.Name+"`", ast.LocOf(field.Name), "")
			continue
		}
		seen[field.Name.Name] = struct{}{}
		valueType := c.typeExpr(scope, field.Value, nil)
		valueType = c.requireValueType(field.Value, valueType, "struct field initializer")
		if typeinfo.IsInvalidOrUnknown(valueType) {
			valueType = &typeinfo.InvalidType{}
		}
		fields = append(fields, typeinfo.Field{Name: field.Name.Name, Type: valueType})
	}
	return &typeinfo.StructType{Fields: fields}
}

func (c *checker) typeArrayLit(scope *table.Scope, node *ast.ArrayLit) typeinfo.Type {
	if node == nil {
		return &typeinfo.InvalidType{}
	}
	arrayType := typeinfo.TypeFromSyntax(node.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
	if typeinfo.IsInvalidOrUnknown(arrayType) {
		return &typeinfo.InvalidType{}
	}
	array, ok := typeinfo.Underlying(arrayType).(*typeinfo.ArrayType)
	if !ok || array == nil || array.Elem == nil || typeinfo.IsInvalidOrUnknown(array.Elem) {
		c.ctx.Diagnostics.Add(invalidTypeError(node.Type, "invalid array literal type"))
		return &typeinfo.InvalidType{}
	}
	if array.Dynamic {
		if c.rejectReferenceStorage(array.Elem, node.Type, "dynamic arrays", true) ||
			c.rejectUnsizedType(array.Elem, node.Type, "dynamic array element") {
			return &typeinfo.InvalidType{}
		}
		if !c.isLowerableType(array.Elem) {
			c.ctx.Diagnostics.Add(invalidTypeError(node.Type,
				"dynamic array element type is not lowerable in current compiler stage"))
			return &typeinfo.InvalidType{}
		}
	}
	if !node.InferredLen {
		if nodeLen, err := strconv.Atoi(array.Len); err == nil && nodeLen != len(node.Values) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrTypeMismatch,
				fmt.Sprintf("array literal has %d values but length is %d", len(node.Values), nodeLen), ast.LocOf(node), "")
		}
	}
	for _, value := range node.Values {
		valueType := c.typeExpr(scope, value, array.Elem)
		valueType = c.requireValueType(value, valueType, "array element initializer")
		if typeinfo.IsInvalidOrUnknown(valueType) {
			continue
		}
		if !c.assignable(array.Elem, valueType) {
			c.ctx.Diagnostics.Add(typeMismatchError(value,
				fmt.Sprintf("cannot assign %s to array element of type %s",
					typeinfo.TypeText(valueType), typeinfo.TypeText(array.Elem))))
		}
	}
	return arrayType
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

func (c *checker) typeAsExpr(scope *table.Scope, node *ast.AsExpr) typeinfo.Type {
	if c == nil || node == nil {
		return nil
	}
	targetType := typeinfo.TypeFromSyntax(node.TypeExpr, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
	if targetType == nil || typeinfo.IsInvalidOrUnknown(targetType) {
		c.ctx.Diagnostics.Add(invalidTypeError(node.TypeExpr, "invalid target type for cast"))
		return &typeinfo.InvalidType{}
	}
	if c.rejectUnsizedType(targetType, node.TypeExpr, "cast target") {
		return &typeinfo.InvalidType{}
	}
	if node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	exprType := c.typeExpr(scope, node.Expr, nil)
	exprType = c.requireValueType(node.Expr, exprType, "cast")
	if typeinfo.IsInvalidOrUnknown(exprType) {
		return &typeinfo.InvalidType{}
	}
	if _, ok := targetType.(*typeinfo.BoolType); ok && typeinfo.IsArithmetic(exprType) {
		return targetType
	}
	compat := typeinfo.CheckNumericCompatibility(targetType, exprType)
	if compat == typeinfo.Incompatible {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidCast,
			fmt.Sprintf("cannot cast %s to %s",
				typeinfo.TypeText(exprType), typeinfo.TypeText(targetType)), ast.LocOf(node), "")
		return &typeinfo.InvalidType{}
	}
	return targetType
}

func (c *checker) typeNumber(node *ast.NumberLit, expected typeinfo.Type) typeinfo.Type {
	if node == nil {
		return nil
	}
	if typeinfo.IsInvalidOrUnknown(expected) {
		expected = nil
	}
	if node.ExplicitType != "" {
		explicit, ok := typeinfo.NumericTypeFromName(node.ExplicitType)
		if !ok {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidNumber,
				fmt.Sprintf("unsupported numeric literal suffix `%s`", node.ExplicitType), ast.LocOf(node), "").
				WithHelp("integer suffix widths must be between 1 and 8388608; float suffixes are limited to f32 and f64")
			return &typeinfo.InvalidType{}
		}
		if !typeinfo.LiteralFitsType(node.Value, explicit) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidNumber,
				fmt.Sprintf("literal `%s%s` does not fit %s", node.Value, node.ExplicitType, node.ExplicitType), ast.LocOf(node), "")
			return &typeinfo.InvalidType{}
		}
		return explicit
	}
	if expected != nil {
		numberTarget := expected
		if optional, ok := typeinfo.Underlying(expected).(*typeinfo.OptionalType); ok && optional != nil && optional.Inner != nil {
			numberTarget = optional.Inner
		}
		naturalType := typeinfo.DefaultNumberType(node.Value)
		if typeinfo.CheckNumericCompatibility(numberTarget, naturalType) == typeinfo.Incompatible {
			c.ctx.Diagnostics.Add(typeMismatchError(node,
				fmt.Sprintf("literal `%s` cannot be used as %s", node.Value, typeinfo.TypeText(expected))))
			return nil
		}
		if !typeinfo.LiteralFitsType(node.Value, numberTarget) {
			d := diagnostics.NewError(fmt.Sprintf("literal `%s` does not fit %s", node.Value, typeinfo.TypeText(numberTarget))).
				WithCode(diagnostics.ErrInvalidNumber).
				WithPrimaryLabel(ast.LocOf(node), "")
			if intType, ok := numberTarget.(*typeinfo.IntegerType); ok {
				d.WithHelp(integerRangeHint(intType))
			}
			c.ctx.Diagnostics.Add(d)
			return nil
		}
		return numberTarget
	}
	return typeinfo.DefaultNumberType(node.Value)
}

func integerRangeHint(t *typeinfo.IntegerType) string {
	if t.Signed {
		bits := t.Bits - 1
		return fmt.Sprintf("%s range: -2^%d to 2^%d-1", typeinfo.TypeText(t), bits, bits)
	}
	return fmt.Sprintf("%s range: 0 to 2^%d-1", typeinfo.TypeText(t), t.Bits)
}

func (c *checker) validBinaryTypes(op string, typ typeinfo.Type) bool {
	switch op {
	case "+", "-", "*", "/":
		return typeinfo.IsArithmetic(typ)
	case "%":
		return typeinfo.IsIntegral(typ)
	case "&", "|", "^", "<<", ">>":
		return typeinfo.IsIntegral(typ)
	case "==", "!=":
		return typeinfo.IsEquatable(typ)
	case "<", "<=", ">", ">=":
		return typeinfo.IsArithmetic(typ)
	case "&&", "||":
		return typeinfo.IsCondition(typ)
	default:
		return false
	}
}

func (c *checker) allowedOp(op string) bool {
	switch op {
	case "+", "-", "*", "/", "%", "&", "|", "^", "<<", ">>", "==", "!=", "<", "<=", ">", ">=", "&&", "||":
		return true
	default:
		return false
	}
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

func (c *checker) matchesReceiverTarget(target, arg typeinfo.Type) bool {
	if c == nil || target == nil || arg == nil {
		return false
	}
	return typeinfo.SameType(target, arg) || c.assignable(target, arg) || c.assignable(arg, target)
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

func Check(ctx *project.CompilerContext, module *project.Module) {
	if module == nil || ctx == nil {
		return
	}
	(&checker{ctx: ctx, module: module}).checkModule()
}
