package typechecker

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func (c *checker) checkFunction(sym *symbols.Symbol, fn *ast.FnDecl) {
	if c == nil || sym == nil || fn == nil {
		return
	}
	c.checkFunctionShape(fn)
	if sym.Scope == nil {
		return
	}
	funcScope := sym.Scope
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

func (c *checker) checkDefaultParameters(scope *symbols.Scope, fn *ast.FnDecl) {
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
		if !typeinfo.IsInvalidOrUnknown(defaultType) && !c.assignable(paramType, defaultType, param.Default) {
			c.ctx.Diagnostics.Add(typeMismatchError(param.Default,
				fmt.Sprintf("cannot implicitly convert %s to %s", typeinfo.TypeText(defaultType), typeinfo.TypeText(paramType))))
		}
		c.rejectOwnedParameterReferences(scope, fn, i, param.Default)
	}
}

func (c *checker) rejectOwnedParameterReferences(scope *symbols.Scope, fn *ast.FnDecl, current int, expr ast.Expr) {
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

func (c *checker) checkFunctionShape(decl *ast.FnDecl) {
	if decl == nil {
		return
	}
	opts := project.TypeSyntaxOptions(c.ctx, c.module, nil, false)
	fnType := typeinfo.FuncTypeFromDeclWithOptions(decl, opts)
	if !c.checkCallableReturn(decl.ReturnType, decl, fnType, decl.ReturnOrigins, false) {
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
		if !typeinfo.IsLowerableType(paramType) {
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

func (c *checker) checkCallableReturn(typeNode ast.TypeExpr, fallback ast.Node, fnType *typeinfo.FuncType, clause *ast.ReturnOriginClause, allowTypeParameters bool) bool {
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
	if !typeinfo.IsLowerableType(typ) && !(allowTypeParameters && typeinfo.ContainsTypeParameter(typ)) {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
			"function return type is not lowerable in current compiler stage", site, "")
		return false
	}
	return true
}

func (c *checker) checkFunctionTypeContracts() {
	ast.ForEachDecl(c.module.AST, func(decl ast.Decl) bool {
		opts := project.TypeSyntaxOptions(c.ctx, c.module, nil, false)
		allowTypeParameters := false
		if typeDecl, ok := decl.(ast.TypeDecl); ok && len(typeDecl.DeclarationTypeParams()) > 0 {
			opts = c.typeDeclSyntaxOptions(typeDecl, false)
			allowTypeParameters = true
		}
		ast.Inspect(decl, func(node ast.Node) bool {
			fnTypeSyntax, ok := node.(*ast.FuncType)
			if !ok || fnTypeSyntax == nil {
				return true
			}
			fnType, _ := typeinfo.TypeFromSyntax(fnTypeSyntax, opts).(*typeinfo.FuncType)
			c.checkCallableReturn(fnTypeSyntax.Return, fnTypeSyntax, fnType, fnTypeSyntax.ReturnOrigins, allowTypeParameters)
			return true
		})
		return true
	})
}

func (c *checker) checkTypeDeclReferenceStorage(decl ast.TypeDecl) {
	if decl == nil {
		return
	}
	opts := c.typeDeclSyntaxOptions(decl, false)
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
	resolvedIface, _ := typeinfo.TypeFromSyntax(iface, c.typeDeclSyntaxOptions(decl, false)).(*typeinfo.InterfaceType)
	allowTypeParameters := len(decl.DeclarationTypeParams()) > 0
	for methodIndex, method := range iface.Methods {
		if method.Name == nil || method.Name.Name == "" {
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "interface method name required", method.Location, "")
			continue
		}
		receiverOpts := c.typeDeclSyntaxOptions(decl, true)
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
		opts := c.typeDeclSyntaxOptions(decl, false)
		for _, param := range method.Params {
			paramType := typeinfo.TypeFromSyntax(param.Type, opts)
			if c.rejectUnsizedType(paramType, param.Type, "interface method parameter") {
				continue
			}
			if c.rejectReferenceStorage(paramType, param.Type, "interface parameter aggregate types", false) {
				continue
			}
			if paramType != nil && !typeinfo.IsLowerableType(paramType) &&
				!(allowTypeParameters && typeinfo.ContainsTypeParameter(paramType)) {
				site := ast.Node(decl)
				if param.Name != nil {
					site = param.Name
				}
				c.ctx.Diagnostics.Add(invalidTypeError(site,
					"interface method parameter type is not lowerable in current compiler stage"))
			}
		}
		if resolvedIface != nil && methodIndex < len(resolvedIface.Methods) {
			c.checkCallableReturn(method.ReturnType, decl, resolvedIface.Methods[methodIndex].CallableType(), method.ReturnOrigins, allowTypeParameters)
		}
	}

}

func (c *checker) typeDeclSyntaxOptions(decl ast.TypeDecl, allowAbstractSelf bool) typeinfo.SyntaxOptions {
	opts := project.TypeSyntaxOptions(c.ctx, c.module, nil, allowAbstractSelf)
	if c == nil || c.module == nil || c.module.ModuleScope == nil || decl == nil || decl.DeclName() == nil {
		return opts
	}
	sym, ok := c.module.ModuleScope.LookupLocal(decl.DeclName().Name)
	if !ok || sym == nil {
		return opts
	}
	defined, ok := sym.Type.(*typeinfo.DefinedType)
	if ok && defined != nil {
		opts.TypeParameters = typeinfo.TypeParameterBindings(defined.TypeParameters, nil)
	}
	return opts
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
			expectedType := typeinfo.TypeFromSyntax(spec.Type, typeinfo.SyntaxOptions{Target: c.ctx.Target, AllowAbstractSelf: true})
			argType := c.typeExpr(c.module.ModuleScope, arg, expectedType)
			if typeinfo.IsInvalidOrUnknown(argType) {
				validArgs = false
				break
			}
			if !typeinfo.SameType(argType, expectedType) &&
				!c.assignable(expectedType, argType, arg) &&
				!c.assignable(argType, expectedType, arg) {
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
