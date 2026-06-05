package typechecker

import (
	"fmt"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/table"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/utils/numeric"
)

type checker struct {
	ctx    *context.CompilerContext
	module *context.Module
}

// --- helpers -----------------------------------------------------------------

func isLowerableType(t typeinfo.Type) bool {
	t = typeinfo.Underlying(t)
	switch typ := t.(type) {
	case *typeinfo.IntegerType, *typeinfo.FloatType, *typeinfo.BoolType, *typeinfo.CStrType:
		return true
	case *typeinfo.RawPtrType:
		return typ != nil && isLowerableType(typ.Target)
	case *typeinfo.StructType:
		if typ == nil {
			return false
		}
		for _, field := range typ.Fields {
			if !isLowerableType(field.Type) {
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
				if containsAbstractSelf(param.Type) || !isLowerableType(param.Type) {
					return false
				}
			}
			if containsAbstractSelf(method.Return) || !isLowerableType(method.Return) {
				return false
			}
		}
		return true
	case *typeinfo.EnumType:
		return typ != nil
	}
	fn, ok := t.(*typeinfo.FuncType)
	if !ok || fn == nil {
		return false
	}
	for _, param := range fn.Params {
		if !isLowerableType(param) {
			return false
		}
	}
	return isLowerableType(fn.Return)
}

func containsAbstractSelf(t typeinfo.Type) bool {
	switch typ := t.(type) {
	case *typeinfo.NamedType:
		return typ != nil && typ.Name == "Self"
	case *typeinfo.RawPtrType:
		return typ != nil && containsAbstractSelf(typ.Target)
	case *typeinfo.FuncType:
		if typ == nil {
			return false
		}
		for _, param := range typ.Params {
			if containsAbstractSelf(param) {
				return true
			}
		}
		return containsAbstractSelf(typ.Return)
	default:
		return false
	}
}

func (c *checker) typeFromSyntax(node ast.TypeExpr) typeinfo.Type {
	return c.typeFromSyntaxWithSelf(node, nil, false)
}

func (c *checker) typeFromSyntaxWithSelf(node ast.TypeExpr, selfType typeinfo.Type, allowAbstractSelf bool) typeinfo.Type {
	if node == nil {
		return nil
	}
	switch typ := node.(type) {
	case *ast.NamedType:
		if typ == nil {
			return nil
		}
		if typ.Name == "Self" {
			if selfType != nil {
				return selfType
			}
			if allowAbstractSelf {
				return &typeinfo.NamedType{Name: "Self"}
			}
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, typ, diagnostics.ErrInvalidType,
				"`Self` can only be used in interface methods and impl blocks")
			return &typeinfo.InvalidType{}
		}
		base := typeinfo.TypeFromSyntax(typ)
		if _, ok := base.(*typeinfo.NamedType); !ok || c == nil || c.module == nil || c.module.ModuleScope == nil {
			return base
		}
		sym, found := c.module.ModuleScope.Lookup(typ.Name)
		if !found || sym == nil || sym.Kind != symbols.SymbolType {
			return base
		}
		sym.Used = true
		resolved, ok := symbols.GetSymbolType(sym)
		if !ok {
			return base
		}
		return resolved
	case *ast.ScopeResolution:
		return c.resolveQualifiedType(typ)
	case *ast.RawPtrType:
		if typ == nil {
			return nil
		}
		return &typeinfo.RawPtrType{Mutable: typ.Mutable, Target: c.typeFromSyntaxWithSelf(typ.Target, selfType, allowAbstractSelf)}
	case *ast.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]typeinfo.Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, c.typeFromSyntaxWithSelf(param, selfType, allowAbstractSelf))
		}
		return &typeinfo.FuncType{
			Params: params,
			Return: c.typeFromSyntaxWithSelf(typ.Return, selfType, allowAbstractSelf),
		}
	case *ast.StructType:
		if typ == nil {
			return nil
		}
		fields := make([]typeinfo.Field, 0, len(typ.Fields))
		for _, field := range typ.Fields {
			name := ""
			if field.Name != nil {
				name = field.Name.Name
			}
			fields = append(fields, typeinfo.Field{Name: name, Type: c.typeFromSyntaxWithSelf(field.Type, selfType, allowAbstractSelf)})
		}
		return &typeinfo.StructType{Fields: fields}
	case *ast.InterfaceType:
		if typ == nil {
			return nil
		}
		methods := make([]typeinfo.Method, 0, len(typ.Methods))
		for _, method := range typ.Methods {
			params := make([]typeinfo.Field, 0, len(method.Params))
			for _, param := range method.Params {
				name := ""
				if param.Name != nil {
					name = param.Name.Name
				}
				params = append(params, typeinfo.Field{Name: name, Type: c.typeFromSyntaxWithSelf(param.Type, selfType, allowAbstractSelf)})
			}
			name := ""
			if method.Name != nil {
				name = method.Name.Name
			}
			methods = append(methods, typeinfo.Method{
				Name:   name,
				Params: params,
				Return: c.typeFromSyntaxWithSelf(method.ReturnType, selfType, allowAbstractSelf),
			})
		}
		return &typeinfo.InterfaceType{Methods: methods}
	default:
		return typeinfo.TypeFromSyntax(node)
	}
}

func (c *checker) resolveQualifiedType(node *ast.ScopeResolution) typeinfo.Type {
	if c == nil || c.module == nil || c.ctx == nil || node == nil || c.module.ModuleScope == nil {
		return typeinfo.TypeFromSyntax(node)
	}
	qualifier := node.Module.Name
	member := node.Name.Name
	imp, ok := c.module.Imports[qualifier]
	if !ok {
		return typeinfo.TypeFromSyntax(node)
	}
	if impSym, ok := c.module.ModuleScope.LookupLocal(qualifier); ok && impSym != nil {
		impSym.Used = true
	}
	mod, ok := c.ctx.ModuleByKey(imp.Key)
	if !ok || mod == nil || mod.ModuleScope == nil {
		return typeinfo.TypeFromSyntax(node)
	}
	sym, found := mod.ModuleScope.LookupLocal(member)
	if !found || sym == nil || sym.Kind != symbols.SymbolType {
		return typeinfo.TypeFromSyntax(node)
	}
	sym.Used = true
	resolved, ok := symbols.GetSymbolType(sym)
	if !ok {
		return typeinfo.TypeFromSyntax(node)
	}
	return resolved
}

func (c *checker) fnTypeFromDecl(decl *ast.FnDecl) *typeinfo.FuncType {
	return c.fnTypeFromDeclWithSelf(decl, nil, false)
}

func (c *checker) fnTypeFromDeclWithSelf(decl *ast.FnDecl, selfType typeinfo.Type, allowAbstractSelf bool) *typeinfo.FuncType {
	if decl == nil {
		return nil
	}
	params := make([]typeinfo.Type, 0, len(decl.Params))
	for _, param := range decl.Params {
		params = append(params, c.typeFromSyntaxWithSelf(param.Type, selfType, allowAbstractSelf))
	}
	return &typeinfo.FuncType{
		Params: params,
		Return: c.typeFromSyntaxWithSelf(decl.ReturnType, selfType, allowAbstractSelf),
	}
}

// -----------------------------------------------------------------------------

func (c *checker) checkModule() {
	if c == nil || c.module == nil || c.module.AST == nil {
		return
	}
	for _, decl := range c.module.AST.Decls {
		switch node := decl.(type) {
		case *ast.FnDecl:
			if node == nil {
				continue
			}
			sym, found := c.module.ModuleScope.Lookup(node.Name.Name)
			if !found || sym == nil {
				continue
			}
			if node.Body == nil {
				sym.BindType(c.fnTypeFromDecl(node))
			} else {
				c.checkFunction(sym, node)
			}
		case *ast.InterfaceDecl:
			c.checkInterfaceDecl(node)
		case *ast.ImplDecl:
			c.checkImplDecl(node)
		}
	}
}

func (c *checker) checkFunction(sym *symbols.Symbol, fn *ast.FnDecl) {
	c.checkFunctionWithSelf(sym, fn, nil, false)
}

func (c *checker) checkFunctionWithSelf(sym *symbols.Symbol, fn *ast.FnDecl, selfType typeinfo.Type, allowAbstractSelf bool) {
	if c == nil || sym == nil || fn == nil || fn.Body == nil {
		return
	}
	c.checkFunctionShapeWithSelf(fn, selfType, allowAbstractSelf)
	sym.BindType(c.fnTypeFromDeclWithSelf(fn, selfType, allowAbstractSelf))
	if sym.Scope == nil {
		return
	}
	funcScope := sym.Scope.(*table.Scope)
	for _, param := range fn.Params {
		if param.Name == nil {
			continue
		}
		paramSym, ok := funcScope.LookupLocal(param.Name.Name)
		if !ok || paramSym == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, fn, diagnostics.ErrUndefinedSymbol, "missing parameter binding")
			return
		}
		paramSym.BindType(c.typeFromSyntaxWithSelf(param.Type, selfType, allowAbstractSelf))
	}
	c.checkBlock(funcScope, fn.Body, c.typeFromSyntaxWithSelf(fn.ReturnType, selfType, allowAbstractSelf))
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
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return
		}
		retType := c.typeExpr(scope, node.Value, returnType)
		if retType == nil {
			return
		}
		if !c.assignable(returnType, retType) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.Value, diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot return %s from function returning %s",
					typeinfo.TypeText(retType), typeinfo.TypeText(returnType)))
		}
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
		} else {
			condType := c.typeExpr(scope, node.Cond, nil)
			if condType != nil && !typeinfo.IsInvalidOrUnknown(condType) && !typeinfo.IsCondition(condType) {
				common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.Cond, diagnostics.ErrInvalidOperation,
					"if condition must be bool or scalar number")
			}
		}
		c.checkBlock(scope, node.Then, returnType)
		c.checkStmt(scope, node.Else, returnType)
	case *ast.ExprStmt:
		if node.Expr == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidStatement,
				"expression statement requires an expression")
			return
		}
		c.typeExpr(scope, node.Expr, nil)
	default:
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidStatement,
			"unsupported statement")
	}
}

func (c *checker) checkBinding(scope *table.Scope, node ast.Stmt, requireInitializer bool) {
	if c == nil || node == nil {
		return
	}
	var (
		nameNode *ast.Ident
		declType typeinfo.Type
		value    ast.Expr
	)
	switch bind := node.(type) {
	case *ast.LetDecl:
		nameNode = bind.Name
		declType = c.typeFromSyntax(bind.Type)
		value = bind.Value
	case *ast.ConstDecl:
		nameNode = bind.Name
		declType = c.typeFromSyntax(bind.Type)
		value = bind.Value
	default:
		return
	}

	// Look up the symbol declared in this exact scope by the resolver.
	sym, found := scope.LookupLocal(nameNode.Name)
	if !found || sym == nil {
		return
	}

	if value == nil {
		if requireInitializer {
			sym.BindType(&typeinfo.InvalidType{})
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrMissingInitializer,
				"missing initializer for const declaration")
			return
		}
		if declType == nil {
			sym.BindType(&typeinfo.InvalidType{})
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrMissingType,
				"let declaration needs type or initializer")
			return
		}
		sym.BindType(declType)
		return
	}

	valType := c.typeExpr(scope, value, declType)
	if valType == nil {
		return
	}
	if declType != nil && !c.assignable(declType, valType) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, value, diagnostics.ErrTypeMismatch,
			fmt.Sprintf("cannot assign %s to %s",
				typeinfo.TypeText(valType), typeinfo.TypeText(declType)))
		sym.BindType(&typeinfo.InvalidType{})
		return
	}
	if declType != nil {
		sym.BindType(declType)
	} else {
		sym.BindType(valType)
	}
}

func (c *checker) checkFunctionShape(decl *ast.FnDecl) {
	c.checkFunctionShapeWithSelf(decl, nil, false)
}

func (c *checker) checkFunctionShapeWithSelf(decl *ast.FnDecl, selfType typeinfo.Type, allowAbstractSelf bool) {
	if decl == nil {
		return
	}
	if !isLowerableType(c.typeFromSyntaxWithSelf(decl.ReturnType, selfType, allowAbstractSelf)) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, decl, diagnostics.ErrInvalidReturn,
			"function return type must be builtin integer, f32/f64, or function type in current compiler stage")
		return
	}
	for _, param := range decl.Params {
		if !isLowerableType(c.typeFromSyntaxWithSelf(param.Type, selfType, allowAbstractSelf)) {
			site := ast.Node(decl)
			if param.Name != nil {
				site = param.Name
			}
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, site, diagnostics.ErrInvalidType,
				"parameter type must be builtin integer, f32/f64, or function type in current compiler stage")
			return
		}
	}
}

func (c *checker) checkInterfaceDecl(decl *ast.InterfaceDecl) {
	if c == nil || decl == nil {
		return
	}
	for _, method := range decl.Methods {
		if method.Name == nil || method.Name.Name == "" {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, decl, diagnostics.ErrMissingIdentifier, "interface method name required")
			continue
		}
		for _, param := range method.Params {
			_ = c.typeFromSyntaxWithSelf(param.Type, nil, true)
		}
		_ = c.typeFromSyntaxWithSelf(method.ReturnType, nil, true)
	}
}

func (c *checker) checkImplDecl(decl *ast.ImplDecl) {
	if c == nil || c.module == nil || c.module.Semantics == nil || decl == nil || decl.Target == nil {
		return
	}
	selfType := c.typeFromSyntax(decl.Target)
	for _, method := range decl.Methods {
		if method == nil {
			continue
		}
		sym, ok := c.module.Semantics.MethodSymbol[method.ID()]
		if !ok || sym == nil {
			continue
		}
		if len(method.Params) == 0 || method.Params[0].Name == nil || method.Params[0].Name.Name != "self" {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, method, diagnostics.ErrInvalidMethodReceiver,
				"impl methods must declare `self` as the first parameter")
			continue
		}
		if method.Body == nil {
			sym.BindType(c.fnTypeFromDeclWithSelf(method, selfType, false))
			continue
		}
		c.checkFunctionWithSelf(sym, method, selfType, false)
	}
}

func (c *checker) assignable(dst, src typeinfo.Type) bool {
	if typeinfo.Assignable(dst, src) {
		return true
	}
	if c == nil {
		return false
	}
	if iface, ok := typeinfo.Underlying(dst).(*typeinfo.InterfaceType); ok && iface != nil {
		return c.satisfiesInterface(iface, src)
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
		requiredType := c.instantiateInterfaceMethod(required, owner)
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
		if !typeinfo.Assignable(fnType.Params[0], src) {
			return false
		}
	}
	return true
}

func (c *checker) interfaceImplementorType(src typeinfo.Type) typeinfo.Type {
	if src == nil {
		return nil
	}
	if ptr, ok := src.(*typeinfo.RawPtrType); ok && ptr != nil && ptr.Target != nil {
		return ptr.Target
	}
	return src
}

func (c *checker) instantiateInterfaceMethod(method typeinfo.Method, ownerType typeinfo.Type) typeinfo.Type {
	params := make([]typeinfo.Type, 0, len(method.Params))
	for _, param := range method.Params {
		t := c.replaceAbstractSelf(param.Type, ownerType)
		params = append(params, t)
	}
	return &typeinfo.FuncType{
		Params: params,
		Return: c.replaceAbstractSelf(method.Return, ownerType),
	}
}

func (c *checker) replaceAbstractSelf(t typeinfo.Type, ownerType typeinfo.Type) typeinfo.Type {
	switch typ := t.(type) {
	case *typeinfo.NamedType:
		if typ != nil && typ.Name == "Self" {
			return ownerType
		}
		return t
	case *typeinfo.RawPtrType:
		if typ == nil {
			return nil
		}
		return &typeinfo.RawPtrType{Mutable: typ.Mutable, Target: c.replaceAbstractSelf(typ.Target, ownerType)}
	case *typeinfo.FuncType:
		if typ == nil {
			return nil
		}
		params := make([]typeinfo.Type, 0, len(typ.Params))
		for _, param := range typ.Params {
			params = append(params, c.replaceAbstractSelf(param, ownerType))
		}
		return &typeinfo.FuncType{Params: params, Return: c.replaceAbstractSelf(typ.Return, ownerType)}
	default:
		return t
	}
}

// typeExpr computes the type of an expression using scope lookup, records it in the
// module's ExprTypes side table for downstream phases, and returns it.
func (c *checker) typeExpr(scope *table.Scope, expr ast.Expr, expected typeinfo.Type) (resolved typeinfo.Type) {
	if expr == nil {
		return nil
	}
	defer func() {
		if resolved != nil && c.module != nil && c.module.Semantics != nil {
			c.module.Semantics.ExprTypes[expr.ID()] = resolved
		}
	}()
	switch node := expr.(type) {
	case *ast.NumberLit:
		return c.typeNumber(node, expected)

	case *ast.StringLit:
		return &typeinfo.CStrType{}

	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok || sym == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrUnknownIdentifier,
				fmt.Sprintf("unknown identifier `%s`\n", node.Name))
			return &typeinfo.InvalidType{}
		}
		t, ok := symbols.GetSymbolType(sym)
		if !ok || t == nil {
			return &typeinfo.UnknownType{}
		}
		return t

	case *ast.ScopeResolution:
		return c.qualifiedScopeType(scope, node)

	case *ast.SelectorExpr:
		return c.typeSelectorExpr(scope, node)

	case *ast.StructLit:
		return c.typeStructLit(scope, node, expected)

	case *ast.UnaryExpr:
		return c.typeUnaryExpr(scope, node, expected)

	case *ast.BinaryExpr:
		return c.typeBinaryExpr(scope, node, expected)

	case *ast.CallExpr:
		return c.typeCallExpr(scope, node, expected)

	case *ast.AsExpr:
		return c.typeAsExpr(scope, node)

	default:
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidExpression,
			"unsupported expression type")
		return nil
	}
}

func (c *checker) typeUnaryExpr(scope *table.Scope, node *ast.UnaryExpr, expected typeinfo.Type) typeinfo.Type {
	if node.Op != "+" && node.Op != "-" && node.Op != "!" {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported unary operator `"+node.Op+"`")
		return nil
	}

	argExpected := typeinfo.Type(nil)
	if node.Op != "!" && expected != nil && typeinfo.IsArithmetic(expected) {
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
	if argType == nil {
		return &typeinfo.InvalidType{}
	}
	if typeinfo.IsInvalidOrUnknown(argType) {
		return &typeinfo.InvalidType{}
	}
	if !typeinfo.IsArithmetic(argType) && !typeinfo.SameType(argType, &typeinfo.BoolType{}) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported unary operand type")
		return nil
	}
	if node.Op == "!" {
		return &typeinfo.BoolType{}
	}
	return argType
}

func (c *checker) typeBinaryExpr(scope *table.Scope, node *ast.BinaryExpr, expected typeinfo.Type) typeinfo.Type {
	if !c.allowedOp(node.Op) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported binary operator `"+node.Op+"`")
		return nil
	}

	left := c.typeExpr(scope, node.Left, expected)
	right := c.typeExpr(scope, node.Right, expected)

	if left == nil || right == nil || typeinfo.IsInvalidOrUnknown(left) || typeinfo.IsInvalidOrUnknown(right) {
		return &typeinfo.InvalidType{}
	}

	commonType := typeinfo.CommonNumericType(left, right)
	if commonType == nil && !c.assignable(left, right) && !c.assignable(right, left) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrTypeMismatch,
			fmt.Sprintf("operand types mismatch: %s vs %s",
				typeinfo.TypeText(left), typeinfo.TypeText(right)))
		return nil
	}

	exprType := left
	if commonType != nil {
		exprType = commonType
	}
	switch node.Op {
	case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
		return &typeinfo.BoolType{}
	}

	if !c.validBinaryTypes(node.Op, left) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported operand type for operator `"+node.Op+"`")
		return nil
	}
	return exprType
}

func (c *checker) typeCallExpr(scope *table.Scope, node *ast.CallExpr, expected typeinfo.Type) typeinfo.Type {
	if selector, ok := node.Callee.(*ast.SelectorExpr); ok && selector != nil {
		return c.typeSelectorCall(scope, selector, node, expected)
	}
	calleeType := c.typeExpr(scope, node.Callee, expected)
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

func (c *checker) typeSelectorExpr(scope *table.Scope, node *ast.SelectorExpr) typeinfo.Type {
	if node == nil || node.Expr == nil || node.Name == nil {
		return &typeinfo.InvalidType{}
	}
	baseType := c.typeExpr(scope, node.Expr, nil)
	if baseType == nil || typeinfo.IsInvalidOrUnknown(baseType) {
		return &typeinfo.InvalidType{}
	}
	if fieldType, ok := c.lookupFieldType(baseType, node.Name.Name); ok {
		return fieldType
	}
	if methodType, _, ok := c.lookupMethodType(baseType, node.Name.Name); ok {
		return methodType
	}
	common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.Name, diagnostics.ErrFieldNotFound,
		fmt.Sprintf("unknown member `%s`", node.Name.Name))
	return &typeinfo.InvalidType{}
}

func (c *checker) typeSelectorCall(scope *table.Scope, selector *ast.SelectorExpr, call *ast.CallExpr, expected typeinfo.Type) typeinfo.Type {
	baseType := c.typeExpr(scope, selector.Expr, nil)
	if baseType == nil || typeinfo.IsInvalidOrUnknown(baseType) {
		return &typeinfo.InvalidType{}
	}
	methodType, methodSym, ok := c.lookupMethodType(baseType, selector.Name.Name)
	if ok {
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
		c.checkMethodCall(call, methodType, argTypes, methodSym)
		return c.callReturnType(call, methodType)
	}
	if _, fieldOK := c.lookupFieldType(baseType, selector.Name.Name); fieldOK {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, selector.Name, diagnostics.ErrNotCallable,
			fmt.Sprintf("field `%s` is not callable", selector.Name.Name))
		return &typeinfo.InvalidType{}
	}
	common.AddError(c.ctx.Diagnostics, c.module.FilePath, selector.Name, diagnostics.ErrMethodNotFound,
		fmt.Sprintf("unknown method `%s`", selector.Name.Name))
	return &typeinfo.InvalidType{}
}

func (c *checker) typeStructLit(scope *table.Scope, node *ast.StructLit, expected typeinfo.Type) typeinfo.Type {
	if node == nil {
		return &typeinfo.InvalidType{}
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
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, field.Name, diagnostics.ErrRedeclaredSymbol,
				"duplicate struct literal field `"+field.Name.Name+"`")
			continue
		}
		fieldsByName[field.Name.Name] = field
	}
	for _, targetField := range targetStruct.Fields {
		field, ok := fieldsByName[targetField.Name]
		if !ok {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrMissingInitializer,
				"missing struct literal field `"+targetField.Name+"`")
			continue
		}
		valueType := c.typeExpr(scope, field.Value, targetField.Type)
		if valueType == nil {
			continue
		}
		if !c.assignable(targetField.Type, valueType) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, field.Value, diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot assign %s to field `%s` of type %s",
					typeinfo.TypeText(valueType), targetField.Name, typeinfo.TypeText(targetField.Type)))
		}
		delete(fieldsByName, targetField.Name)
	}
	for name, field := range fieldsByName {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, field.Name, diagnostics.ErrFieldNotFound,
			"unknown struct literal field `"+name+"`")
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
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, field.Name, diagnostics.ErrRedeclaredSymbol,
				"duplicate struct literal field `"+field.Name.Name+"`")
			continue
		}
		seen[field.Name.Name] = struct{}{}
		valueType := c.typeExpr(scope, field.Value, nil)
		if valueType == nil {
			valueType = &typeinfo.InvalidType{}
		}
		fields = append(fields, typeinfo.Field{Name: field.Name.Name, Type: valueType})
	}
	return &typeinfo.StructType{Fields: fields}
}

func (c *checker) lookupFieldType(baseType typeinfo.Type, name string) (typeinfo.Type, bool) {
	if ptr, ok := baseType.(*typeinfo.RawPtrType); ok && ptr != nil && ptr.Target != nil {
		baseType = ptr.Target
	}
	underlying := typeinfo.Underlying(baseType)
	strct, ok := underlying.(*typeinfo.StructType)
	if !ok || strct == nil {
		return nil, false
	}
	for _, field := range strct.Fields {
		if field.Name == name {
			return field.Type, true
		}
	}
	return nil, false
}

func (c *checker) lookupMethodType(baseType typeinfo.Type, name string) (typeinfo.Type, *symbols.Symbol, bool) {
	if c == nil || c.module == nil || c.module.Semantics == nil {
		return nil, nil, false
	}
	if iface, ok := typeinfo.Underlying(baseType).(*typeinfo.InterfaceType); ok && iface != nil {
		for _, method := range iface.Methods {
			if method.Name != name {
				continue
			}
			return c.boundInterfaceMethodType(method, baseType), nil, true
		}
	}
	keys := make([]string, 0, 4)
	appendKey := func(typ typeinfo.Type) {
		if typ == nil {
			return
		}
		key := typeinfo.TypeText(typ)
		if key == "" {
			return
		}
		for _, existing := range keys {
			if existing == key {
				return
			}
		}
		keys = append(keys, key)
	}
	appendKey(baseType)
	if underlying := typeinfo.Underlying(baseType); underlying != baseType {
		appendKey(underlying)
	}
	if ptr, ok := baseType.(*typeinfo.RawPtrType); ok && ptr != nil && ptr.Target != nil {
		appendKey(ptr.Target)
		if underlying := typeinfo.Underlying(ptr.Target); underlying != ptr.Target {
			appendKey(underlying)
		}
	}
	for _, key := range keys {
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

func (c *checker) boundInterfaceMethodType(method typeinfo.Method, receiverType typeinfo.Type) typeinfo.Type {
	params := make([]typeinfo.Type, 0, len(method.Params))
	for i, param := range method.Params {
		t := c.replaceAbstractSelf(param.Type, receiverType)
		if i == 0 {
			t = receiverType
		}
		params = append(params, t)
	}
	return &typeinfo.FuncType{
		Params: params,
		Return: c.replaceAbstractSelf(method.Return, receiverType),
	}
}

func (c *checker) callReturnType(call *ast.CallExpr, calleeType typeinfo.Type) typeinfo.Type {
	if c == nil {
		return &typeinfo.InvalidType{}
	}
	if calleeType != nil {
		if fnType, ok := calleeType.(*typeinfo.FuncType); ok && fnType != nil {
			if fnType.Return != nil && !typeinfo.IsUnknown(fnType.Return) {
				return fnType.Return
			}
			if call != nil && !typeinfo.IsInvalidOrUnknown(fnType.Return) {
				common.AddError(c.ctx.Diagnostics, c.module.FilePath, call, diagnostics.ErrInvalidType,
					"call has unknown return type")
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
	targetType := c.typeFromSyntax(node.TypeExpr)
	if targetType == nil || typeinfo.IsInvalidOrUnknown(targetType) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.TypeExpr, diagnostics.ErrInvalidType,
			"invalid target type for cast")
		return &typeinfo.InvalidType{}
	}
	if node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	exprType := c.typeExpr(scope, node.Expr, nil)
	if exprType == nil || typeinfo.IsInvalidOrUnknown(exprType) {
		return &typeinfo.InvalidType{}
	}
	compat := typeinfo.CheckNumericCompatibility(targetType, exprType)
	if compat == typeinfo.Incompatible {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidCast,
			fmt.Sprintf("cannot cast %s to %s",
				typeinfo.TypeText(exprType), typeinfo.TypeText(targetType)))
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
	if expected != nil {
		naturalType := typeinfo.DefaultNumberType(node.Value)
		if typeinfo.CheckNumericCompatibility(expected, naturalType) == typeinfo.Incompatible {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrTypeMismatch,
				fmt.Sprintf("literal `%s` cannot be used as %s", node.Value, typeinfo.TypeText(expected)))
			return nil
		}
		if !literalFitsType(node.Value, expected) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidNumber,
				fmt.Sprintf("literal `%s` does not fit %s", node.Value, typeinfo.TypeText(expected)))
			return nil
		}
		return expected
	}
	return typeinfo.DefaultNumberType(node.Value)
}

func literalFitsType(value string, typ typeinfo.Type) bool {
	switch t := typ.(type) {
	case *typeinfo.IntegerType:
		return numeric.FitsIntegerLiteral(value, t.Bits, t.Signed)
	case *typeinfo.FloatType:
		if numeric.IsFloat(value) {
			return numeric.FitsFloatLiteral(value, t.Bits)
		}
		return numeric.FitsIntegerLiteralInFloat(value, t.Bits)
	}
	return false
}

func (c *checker) validBinaryTypes(op string, typ typeinfo.Type) bool {
	switch op {
	case "+", "-", "*", "/":
		return typeinfo.IsArithmetic(typ)
	case "%":
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
	case "+", "-", "*", "/", "%", "==", "!=", "<", "<=", ">", ">=", "&&", "||":
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
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, callExpr, diagnostics.ErrNotCallable,
				"call target is not a function")
		}
		return
	}
	if len(args) != len(fnType.Params) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, callExpr, diagnostics.ErrWrongArgumentCount,
			fmt.Sprintf("wrong number of arguments: got %d, want %d", len(args), len(fnType.Params)))
		return
	}
	for i, argType := range args {
		if argType == nil {
			continue
		}
		paramType := fnType.Params[i]
		if paramType == nil {
			continue
		}
		if !c.assignable(paramType, argType) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, callExpr.Args[i], diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot implicitly convert %s to %s",
					typeinfo.TypeText(argType), typeinfo.TypeText(paramType)))
		}
	}
}

func (c *checker) checkMethodCall(callExpr *ast.CallExpr, calleeType typeinfo.Type, args []typeinfo.Type, _ *symbols.Symbol) {
	if c == nil || callExpr == nil || calleeType == nil {
		return
	}
	fnType, ok := calleeType.(*typeinfo.FuncType)
	if !ok || fnType == nil {
		if !typeinfo.IsInvalidOrUnknown(calleeType) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, callExpr, diagnostics.ErrNotCallable,
				"call target is not a method")
		}
		return
	}
	if len(args) != len(fnType.Params) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, callExpr, diagnostics.ErrWrongArgumentCount,
			fmt.Sprintf("wrong number of arguments: got %d, want %d", len(args)-1, len(fnType.Params)-1))
		return
	}
	for i, argType := range args {
		if argType == nil {
			continue
		}
		paramType := fnType.Params[i]
		if paramType == nil {
			continue
		}
		if !c.assignable(paramType, argType) {
			site := ast.Node(callExpr)
			if i > 0 && i-1 < len(callExpr.Args) {
				site = callExpr.Args[i-1]
			}
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, site, diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot implicitly convert %s to %s",
					typeinfo.TypeText(argType), typeinfo.TypeText(paramType)))
		}
	}
}

// qualifiedScopeType resolves the type of a `module::symbol` expression using ScopeResolution.
func (c *checker) qualifiedScopeType(scope *table.Scope, node *ast.ScopeResolution) typeinfo.Type {
	_ = scope // the current scope is not used for two-step foreign lookup
	alias := node.Module.Name
	member := node.Name.Name
	imp, ok := c.module.Imports[alias]
	if !ok {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrModuleNotFound,
			"unknown import alias `"+alias+"`")
		return &typeinfo.InvalidType{}
	}
	mod, ok := c.ctx.ModuleByKey(imp.Key)
	if !ok || mod == nil || mod.ModuleScope == nil {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrModuleNotFound,
			"module `"+alias+"` not loaded")
		return &typeinfo.InvalidType{}
	}
	sym, ok := mod.ModuleScope.LookupLocal(member)
	if !ok || sym == nil {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrUndefinedSymbol,
			"unknown identifier `"+member+"` in module `"+alias+"`")
		return &typeinfo.InvalidType{}
	}
	t, ok := symbols.GetSymbolType(sym)
	if !ok || t == nil {
		return &typeinfo.UnknownType{}
	}
	return t
}

func Check(ctx *context.CompilerContext, module *context.Module) {
	if module == nil || ctx == nil {
		return
	}
	(&checker{ctx: ctx, module: module}).checkModule()
}
