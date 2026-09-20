package typechecker

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/problems"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

func (c *checker) checkFunction(sym *symbols.Symbol, fn *ast.FnDecl) {
	if c == nil || sym == nil || fn == nil {
		return
	}
	fnType, ok := sym.Type.(*typeinfo.FuncType)
	if !ok || fnType == nil {
		return
	}
	c.checkFunctionShape(fn, fnType)
	if sym.Scope == nil {
		return
	}
	funcScope := sym.Scope
	for index, param := range fn.ParamsWithReceiver() {
		if param.Name == nil || index >= len(fnType.Params) {
			continue
		}
		paramSym := c.module.Bindings.Symbol(param.Name)
		if paramSym == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "missing parameter binding", ast.LocOf(param.Name), "")
			return
		}
		paramSym.BindType(fnType.Params[index])
	}
	c.checkDefaultParameters(funcScope, fn, fnType)
	if fn.Body != nil {
		c.checkBlock(funcScope, fn.Body, fnType.Return)
	}
}

func (c *checker) checkDefaultParameters(scope *symbols.Scope, fn *ast.FnDecl, fnType *typeinfo.FuncType) {
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
		if i >= len(fnType.Params) {
			continue
		}
		paramType := fnType.Params[i]
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
		c.rejectOwnedParameterReferences(scope, fn, fnType, i, param.Default)
	}
}

func (c *checker) rejectOwnedParameterReferences(scope *symbols.Scope, fn *ast.FnDecl, fnType *typeinfo.FuncType, current int, expr ast.Expr) {
	if c == nil || c.module == nil || c.module.Bindings == nil || fn == nil || expr == nil {
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
		sym := c.module.Bindings.Symbol(ident)
		index, isParam := paramIndexes[sym]
		if !isParam || index >= current || index < 0 || index >= len(params) {
			return true
		}
		if index >= len(fnType.Params) {
			return true
		}
		paramType := fnType.Params[index]
		if typeinfo.OwnershipCapabilityOf(paramType).Copy == typeinfo.CopyImplicit {
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

func (c *checker) checkFunctionShape(decl *ast.FnDecl, fnType *typeinfo.FuncType) {
	if decl == nil || fnType == nil {
		return
	}
	if _, external := ast.FunctionLinkName(decl, ""); external {
		type externTypeSite struct {
			typ  typeinfo.Type
			site ast.Node
		}
		types := make([]externTypeSite, 0, len(fnType.Params)+1)
		if fnType.Return != nil {
			types = append(types, externTypeSite{typ: fnType.Return, site: decl.ReturnType})
		}
		params := decl.ParamsWithReceiver()
		for index, typ := range fnType.Params {
			site := ast.Node(decl)
			if index < len(params) && params[index].Type != nil {
				site = params[index].Type
			}
			types = append(types, externTypeSite{typ: typ, site: site})
		}
		for _, entry := range types {
			if !typeinfo.ContainsNamedEnum(entry.typ) {
				continue
			}
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidType,
				"named enums cannot cross extern boundaries", ast.LocOf(entry.site),
				"define a foreign representation after enum FFI rules are specified")
		}
	}
	if !c.checkCallableReturn(decl.ReturnType, decl, fnType, decl.ReturnOrigins, false) {
		return
	}
	for index, param := range decl.ParamsWithReceiver() {
		if index >= len(fnType.Params) {
			continue
		}
		paramType := fnType.Params[index]
		if typeinfo.ContainsInvalid(paramType) {
			continue
		}
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
		context := project.TypeContext{}
		allowTypeParameters := false
		if typeDecl, ok := decl.(ast.TypeDecl); ok && len(typeDecl.DeclarationTypeParams()) > 0 {
			context = c.typeContextForDecl(typeDecl, false)
			allowTypeParameters = true
		}
		ast.Inspect(decl, func(node ast.Node) bool {
			fnTypeSyntax, ok := node.(*ast.FuncType)
			if !ok || fnTypeSyntax == nil {
				return true
			}
			fnType, _ := project.ResolveType(c.ctx, c.module, fnTypeSyntax, context).(*typeinfo.FuncType)
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
	context := c.typeContextForDecl(decl, false)
	switch node := decl.(type) {
	case *ast.StructDecl:
		strct, ok := node.Type.(*ast.StructType)
		if !ok || strct == nil {
			return
		}
		for _, field := range strct.Fields {
			fieldType := project.ResolveType(c.ctx, c.module, field.Type, context)
			c.rejectReferenceStorage(fieldType, field.Type, "struct fields", true)
			c.rejectUnsizedType(fieldType, field.Type, "struct field")
		}
	case *ast.TypeAliasDecl:
		typ := project.ResolveType(c.ctx, c.module, node.Type, context)
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
	resolvedIface, _ := project.ResolveType(c.ctx, c.module, iface, c.typeContextForDecl(decl, false)).(*typeinfo.InterfaceType)
	allowTypeParameters := len(decl.DeclarationTypeParams()) > 0
	for methodIndex, method := range iface.Methods {
		if method.Name == nil || method.Name.Name == "" {
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "interface method name required", method.Location, "")
			continue
		}
		if method.Receiver == nil {
			c.ctx.Diagnostics.Add(invalidTypeError(method.Name,
				"iface methods require Self, &Self, or &mut Self receiver"))
			continue
		}
		if resolvedIface != nil && methodIndex < len(resolvedIface.Methods) &&
			resolvedIface.Methods[methodIndex].Receiver == typeinfo.MethodReceiverInvalid {
			c.ctx.Diagnostics.Add(invalidTypeError(method.Receiver.Type,
				"iface method receiver must be Self, &Self, or &mut Self"))
		}
		context := c.typeContextForDecl(decl, false)
		for _, param := range method.Params {
			paramType := project.ResolveType(c.ctx, c.module, param.Type, context)
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

func (c *checker) checkEnumDecl(decl *ast.EnumDecl) {
	if c == nil || decl == nil {
		return
	}
	enumType, ok := decl.Type.(*ast.EnumType)
	if !ok || enumType == nil {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidTypeInParser, "enum declaration missing enum payload", ast.LocOf(decl), "")
		return
	}
	if len(enumType.Variants) == 0 {
		c.ctx.Diagnostics.Add(invalidTypeError(decl, "enum requires at least one variant"))
		return
	}
	context := c.typeContextForDecl(decl, false)
	allowTypeParameters := len(decl.DeclarationTypeParams()) > 0
	dataFields := make(map[string]*source.Location)
	for _, variant := range enumType.Variants {
		if variant.Name == nil || variant.Name.Name == "" {
			continue
		}
		if !symbols.IsPubName(variant.Name.Name) || strings.Contains(variant.Name.Name, "_") {
			c.ctx.Diagnostics.Add(invalidTypeError(variant.Name, "variant name must be PascalCase"))
		}
		if variant.Payload == nil {
			continue
		}
		payloadType := project.ResolveType(c.ctx, c.module, variant.Payload, context)
		payload, isStruct := typeinfo.Underlying(payloadType).(*typeinfo.StructType)
		inlinePayload, inline := variant.Payload.(*ast.StructType)
		if !isStruct {
			c.checkEnumPayloadType(variant.Payload, context, allowTypeParameters, "enum variant payload")
			continue
		}
		if inline && len(inlinePayload.Fields) == 0 {
			c.ctx.Diagnostics.Add(invalidTypeError(variant.Name, "variant data requires at least one field"))
			continue
		}
		if !inline {
			c.checkEnumPayloadType(variant.Payload, context, allowTypeParameters, "enum variant payload")
			for _, field := range payload.Fields {
				if field.Name == "" {
					continue
				}
				if dataFields[field.Name] == nil {
					dataFields[field.Name] = ast.LocOf(variant.Payload)
				}
			}
			continue
		}
		variantFields := make(map[string]*ast.Ident, len(inlinePayload.Fields))
		for _, field := range inlinePayload.Fields {
			if field.Name == nil || field.Name.Name == "" {
				continue
			}
			if previous := variantFields[field.Name.Name]; previous != nil {
				message := fmt.Sprintf("variant field `%s` already declared", field.Name.Name)
				c.ctx.Diagnostics.Add(problems.Redeclaration(message, field.Name.Location, previous.Location))
				continue
			}
			variantFields[field.Name.Name] = field.Name
			if dataFields[field.Name.Name] == nil {
				dataFields[field.Name.Name] = field.Name.Location
			}
			c.checkEnumPayloadType(field.Type, context, allowTypeParameters, "enum variant field")
		}
	}
	if decl.Name == nil {
		return
	}
	if c.module == nil || c.module.Bindings == nil {
		return
	}
	declSymbol := c.module.Bindings.Symbol(decl.Name)
	if declSymbol == nil {
		return
	}
	for _, method := range c.module.Bindings.Methods(declSymbol.Type) {
		if method == nil || dataFields[method.Name] == nil {
			continue
		}
		message := fmt.Sprintf("method `%s` conflicts with enum variant data field", method.Name)
		c.ctx.Diagnostics.Add(problems.Redeclaration(message, method.Location, dataFields[method.Name]))
	}
}

func (c *checker) checkEnumPayloadType(syntax ast.TypeExpr, typeContext project.TypeContext, allowTypeParameters bool, context string) {
	payloadType := project.ResolveType(c.ctx, c.module, syntax, typeContext)
	if c.rejectUnsizedType(payloadType, syntax, context) {
		return
	}
	if c.rejectReferenceStorage(payloadType, syntax, context+"s", false) {
		return
	}
	if !typeinfo.IsLowerableType(payloadType) && !(allowTypeParameters && typeinfo.ContainsTypeParameter(payloadType)) {
		c.ctx.Diagnostics.Add(invalidTypeError(syntax, context+" type is not lowerable in current compiler stage"))
	}
}

func (c *checker) typeContextForDecl(decl ast.TypeDecl, allowAbstractSelf bool) project.TypeContext {
	context := project.TypeContext{AllowAbstractSelf: allowAbstractSelf}
	if iface, ok := decl.(*ast.InterfaceDecl); ok {
		context.NamedInterfaceRoot = iface.UnderlyingType()
	}
	if c == nil || c.module == nil || c.module.ModuleScope == nil || decl == nil || decl.DeclName() == nil {
		return context
	}
	sym, ok := c.module.ModuleScope.LookupLocal(decl.DeclName().Name)
	if !ok || sym == nil {
		return context
	}
	defined, ok := sym.Type.(*typeinfo.DefinedType)
	if ok && defined != nil {
		context.TypeParameters = typeinfo.TypeParameterBindings(defined.TypeParameters, nil)
	}
	return context
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
	receiverType := project.ResolveType(c.ctx, c.module, fn.Receiver.Type, project.TypeContext{})
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
			expectedType := project.ResolveType(c.ctx, c.module, spec.Type, project.TypeContext{AllowAbstractSelf: true})
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
