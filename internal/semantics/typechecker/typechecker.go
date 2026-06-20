package typechecker

import (
	"fmt"
	"strings"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/numeric"
)

type checker struct {
	ctx    *project.CompilerContext
	module *project.Module
}

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

func (c *checker) isLowerableType(t typeinfo.Type) bool {
	switch typ := typeinfo.Underlying(t).(type) {
	case *typeinfo.IntegerType, *typeinfo.FloatType, *typeinfo.BoolType, *typeinfo.CStrType:
		return true
	case *typeinfo.RawPtrType:
		return typ != nil && typ.Target != nil
	case *typeinfo.StructType:
		if typ == nil {
			return false
		}
		for _, field := range typ.Fields {
			if !c.isLowerableType(field.Type) {
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
				if typeinfo.ContainsAbstractSelf(param.Type) || !c.isLowerableType(param.Type) {
					return false
				}
			}
			if method.Return != nil && (typeinfo.ContainsAbstractSelf(method.Return) || !c.isLowerableType(method.Return)) {
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
		if !c.isLowerableType(param) {
			return false
		}
	}
	return fn.Return == nil || c.isLowerableType(fn.Return)
}

func isValidReceiverType(paramType, selfType typeinfo.Type) bool {
	if paramType == nil || selfType == nil {
		return false
	}
	if typeinfo.SameType(paramType, selfType) {
		return true
	}
	ptr, ok := paramType.(*typeinfo.RawPtrType)
	if !ok || ptr == nil || ptr.Target == nil {
		return false
	}
	return typeinfo.SameType(ptr.Target, selfType)
}

// -----------------------------------------------------------------------------

func (c *checker) checkModule() {
	if c == nil || c.module == nil || c.module.AST == nil {
		return
	}
	for _, stmt := range c.module.AST.Stmts {
		decl, ok := stmt.(ast.Decl) // ? Why even needed?
		if !ok {
			continue
		}
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
	}
	for _, stmt := range c.module.AST.Stmts {
		decl, ok := stmt.(ast.Decl) // ? Why even needed?
		if !ok {
			continue
		}
		switch node := decl.(type) {
		case *ast.InterfaceDecl:
			c.checkInterfaceDecl(node)
		}
	}
	for _, stmt := range c.module.AST.Stmts {
		decl, ok := stmt.(ast.Decl)
		if !ok {
			continue
		}
		switch node := decl.(type) {
		case *ast.FnDecl:
			if node == nil {
				continue
			}
			sym, found := c.module.ModuleScope.Lookup(node.Name.Name)
			if !found || sym == nil {
				continue
			}
			if node.Body != nil {
				c.checkFunctionWithSelf(sym, node, nil, false)
			}
		case *ast.ImplDecl:
			c.checkImplDecl(node)
		}
	}
}

func (c *checker) checkFunctionWithSelf(sym *symbols.Symbol, fn *ast.FnDecl, selfType typeinfo.Type, allowAbstractSelf bool) {
	if c == nil || sym == nil || fn == nil || fn.Body == nil {
		return
	}
	c.checkFunctionShapeWithSelf(fn, selfType, allowAbstractSelf)
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
			c.ctx.Diagnostics.AddError(diagnostics.ErrUndefinedSymbol, "missing parameter binding", ast.LocOf(fn), "")
			return
		}
		paramSym.BindType(typeinfo.ASTTypeWithOptions(param.Type, project.TypeSyntaxOptions(c.ctx, c.module, selfType, allowAbstractSelf)))
	}
	c.checkBlock(funcScope, fn.Body, typeinfo.ASTTypeWithOptions(fn.ReturnType, project.TypeSyntaxOptions(c.ctx, c.module, selfType, allowAbstractSelf)))
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
		case symbols.SymbolVar:
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
		if ptrType, ok := baseType.(*typeinfo.RawPtrType); ok && ptrType != nil {
			return
		}
		if c.isMutableAddressableExpr(scope, target.Expr) {
			return
		}
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"field assignment requires a mutable pointer or mutable local binding", ast.LocOf(target), "")
		return
	default:
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment,
			"invalid assignment target", ast.LocOf(node.Target), "")
	}
}

func (c *checker) checkBinding(scope *table.Scope, node ast.Stmt, requireInitializer bool) {
	if c == nil || node == nil {
		return
	}
	var (
		nameNode *ast.Ident
		declType typeinfo.Type
		typeNode ast.TypeExpr // AST node for the type annotation (for diagnostics)
		value    ast.Expr
	)
	switch bind := node.(type) {
	case *ast.LetDecl:
		nameNode = bind.Name
		declType = typeinfo.ASTTypeWithOptions(bind.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
		typeNode = bind.Type
		value = bind.Value
	case *ast.ConstDecl:
		nameNode = bind.Name
		declType = typeinfo.ASTTypeWithOptions(bind.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
		typeNode = bind.Type
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
		sym.BindType(declType)
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

func (c *checker) checkFunctionShapeWithSelf(decl *ast.FnDecl, selfType typeinfo.Type, allowAbstractSelf bool) {
	if decl == nil {
		return
	}
	if retType := typeinfo.ASTTypeWithOptions(decl.ReturnType, project.TypeSyntaxOptions(c.ctx, c.module, selfType, allowAbstractSelf)); retType != nil && !c.isLowerableType(retType) {
		c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
			"function return type is not lowerable in current compiler stage", ast.LocOf(decl), "")
		return
	}
	for _, param := range decl.Params {
		if !c.isLowerableType(typeinfo.ASTTypeWithOptions(param.Type, project.TypeSyntaxOptions(c.ctx, c.module, selfType, allowAbstractSelf))) {
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
	for _, method := range iface.Methods {
		if method.Name == nil || method.Name.Name == "" {
			c.ctx.Diagnostics.AddError(diagnostics.ErrMissingIdentifier, "interface method name required", ast.LocOf(decl), "")
			continue
		}
		opts := project.TypeSyntaxOptions(c.ctx, c.module, nil, true)
		for _, param := range method.Params {
			paramType := typeinfo.ASTTypeWithOptions(param.Type, opts)
			// Abstract Self is resolved at impl time; skip lowerability check.
			if paramType != nil && !typeinfo.ContainsAbstractSelf(paramType) && !c.isLowerableType(paramType) {
				site := ast.Node(decl)
				if param.Name != nil {
					site = param.Name
				}
				c.ctx.Diagnostics.Add(invalidTypeError(site,
					"interface method parameter type is not lowerable in current compiler stage"))
			}
		}
		if retType := typeinfo.ASTTypeWithOptions(method.ReturnType, opts); retType != nil &&
			!typeinfo.ContainsAbstractSelf(retType) && !c.isLowerableType(retType) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidReturn,
				"interface method return type is not lowerable in current compiler stage", ast.LocOf(decl), "")
		}
	}

}

func (c *checker) checkImplDecl(decl *ast.ImplDecl) {
	if c == nil || c.module == nil || c.module.Semantics == nil || decl == nil || decl.Target == nil {
		return
	}
	selfType := typeinfo.ASTTypeWithOptions(decl.Target, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
	for _, method := range decl.Methods {
		if method == nil {
			continue
		}
		sym, ok := c.module.Semantics.MethodSymbol[method.ID()]
		if !ok || sym == nil {
			continue
		}
		errmsg := "impl methods must declare a `Self` receiver as the first parameter"
		if len(method.Params) == 0 {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidMethodReceiver, errmsg, ast.LocOf(method), "").
				WithSecondaryLabel(ast.LocOf(decl.Target), fmt.Sprintf("impl target is %s", typeinfo.TypeText(selfType))).
				WithHelp(fmt.Sprintf("first parameter should be 'self: %s' or 'self: ^%s'", typeinfo.TypeText(selfType), typeinfo.TypeText(selfType)))
			continue
		}
		firstParamType := typeinfo.ASTTypeWithOptions(method.Params[0].Type, project.TypeSyntaxOptions(c.ctx, c.module, selfType, true))
		if !isValidReceiverType(firstParamType, selfType) {
			c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidMethodReceiver, errmsg, ast.LocOf(method), "").
				WithSecondaryLabel(ast.LocOf(decl.Target), fmt.Sprintf("impl target is %s", typeinfo.TypeText(selfType))).
				WithHelp(fmt.Sprintf("first parameter should be 'self: %s' or 'self: ^%s'", typeinfo.TypeText(selfType), typeinfo.TypeText(selfType)))
			continue
		}

		if method.Body != nil {
			c.checkFunctionWithSelf(sym, method, selfType, false)
		}
	}
}

func (c *checker) assignable(dst, src typeinfo.Type) bool {
	if c == nil {
		return typeinfo.Assignable(dst, src)
	}
	if typeinfo.Assignable(dst, src) {
		return true
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
	iface, ok := typeinfo.Underlying(dst).(*typeinfo.InterfaceType)
	if !ok || iface == nil {
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
	if ptr, ok := src.(*typeinfo.RawPtrType); ok && ptr != nil && ptr.Target != nil {
		return ptr.Target
	}
	return src
}

func (c *checker) instantiateInterfaceMethod(method typeinfo.Method, ownerType typeinfo.Type) typeinfo.Type {
	params := make([]typeinfo.Type, 0, len(method.Params))
	for _, param := range method.Params {
		t := typeinfo.ReplaceAbstractSelf(param.Type, ownerType)
		params = append(params, t)
	}
	return &typeinfo.FuncType{
		Params: params,
		Return: typeinfo.ReplaceAbstractSelf(method.Return, ownerType),
	}
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

	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok || sym == nil {
			c.ctx.Diagnostics.AddError(diagnostics.ErrUnknownIdentifier,
				fmt.Sprintf("unknown identifier `%s`\n", node.Name), ast.LocOf(node), "")
			return &typeinfo.InvalidType{}
		}
		if sym.Initializing || (!sym.Initialized && symbols.RequiresInitialization(sym.Kind)) {
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
		return nil // resolver already diagnosed unsupported expressions
	}
}

func (c *checker) typeUnaryExpr(scope *table.Scope, node *ast.UnaryExpr, expected typeinfo.Type) typeinfo.Type {
	if node.Op != "+" && node.Op != "-" && node.Op != "!" {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported unary operator `"+node.Op+"`"))
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
	if !typeinfo.IsArithmetic(argType) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported unary operand type"))
		return nil
	}
	return argType
}

func (c *checker) typeBinaryExpr(scope *table.Scope, node *ast.BinaryExpr, expected typeinfo.Type) typeinfo.Type {
	if !c.allowedOp(node.Op) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported binary operator `"+node.Op+"`"))
		return nil
	}

	left := c.typeExpr(scope, node.Left, expected)
	right := c.typeExpr(scope, node.Right, expected)
	left = c.requireValueType(node.Left, left, "left operand")
	right = c.requireValueType(node.Right, right, "right operand")

	if typeinfo.IsInvalidOrUnknown(left) || typeinfo.IsInvalidOrUnknown(right) {
		return &typeinfo.InvalidType{}
	}

	commonType := typeinfo.CommonNumericType(left, right)
	if commonType == nil && !c.assignable(left, right) && !c.assignable(right, left) {
		c.ctx.Diagnostics.Add(typeMismatchError(node,
			fmt.Sprintf("operand types mismatch: %s vs %s",
				typeinfo.TypeText(left), typeinfo.TypeText(right))))
		return nil
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
		return &typeinfo.BoolType{}
	}

	if !c.validBinaryTypes(node.Op, left) {
		c.ctx.Diagnostics.Add(invalidOperationError(node,
			"unsupported operand type for operator `"+node.Op+"`"))
		return nil
	}
	return exprType
}

func (c *checker) typeCallExpr(scope *table.Scope, node *ast.CallExpr, expected typeinfo.Type) typeinfo.Type {
	if selector, ok := node.Callee.(*ast.SelectorExpr); ok && selector != nil {
		return c.typeSelectorCall(scope, selector, node)
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

func (c *checker) typeSelectorCall(scope *table.Scope, selector *ast.SelectorExpr, call *ast.CallExpr) typeinfo.Type {
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
		c.checkMethodCall(scope, selector.Expr, call, methodType, argTypes, methodSym)
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

func (c *checker) typeStructLit(scope *table.Scope, node *ast.StructLit, expected typeinfo.Type) typeinfo.Type {
	if node == nil {
		return &typeinfo.InvalidType{}
	}
	if node.Type != nil {
		targetType := typeinfo.ASTTypeWithOptions(node.Type, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
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
	if iface, ok := typeinfo.Underlying(baseType).(*typeinfo.InterfaceType); ok && iface != nil {
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
	params := make([]typeinfo.Type, 0, len(method.Params))
	for i, param := range method.Params {
		t := typeinfo.ReplaceAbstractSelf(param.Type, receiverType)
		if i == 0 {
			t = receiverType
		}
		params = append(params, t)
	}
	return &typeinfo.FuncType{
		Params: params,
		Return: typeinfo.ReplaceAbstractSelf(method.Return, receiverType),
	}
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
	targetType := typeinfo.ASTTypeWithOptions(node.TypeExpr, project.TypeSyntaxOptions(c.ctx, c.module, nil, false))
	if targetType == nil || typeinfo.IsInvalidOrUnknown(targetType) {
		c.ctx.Diagnostics.Add(invalidTypeError(node.TypeExpr, "invalid target type for cast"))
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
	if expected != nil {
		naturalType := typeinfo.DefaultNumberType(node.Value)
		if typeinfo.CheckNumericCompatibility(expected, naturalType) == typeinfo.Incompatible {
			c.ctx.Diagnostics.Add(typeMismatchError(node,
				fmt.Sprintf("literal `%s` cannot be used as %s", node.Value, typeinfo.TypeText(expected))))
			return nil
		}
		if !literalFitsType(node.Value, expected) {
			d := diagnostics.NewError(fmt.Sprintf("literal `%s` does not fit %s", node.Value, typeinfo.TypeText(expected))).
				WithCode(diagnostics.ErrInvalidNumber).
				WithPrimaryLabel(ast.LocOf(node), "")
			if intType, ok := expected.(*typeinfo.IntegerType); ok {
				d.WithHelp(integerRangeHint(intType))
			}
			c.ctx.Diagnostics.Add(d)
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
		}
	}
}

func (c *checker) isMutableAddressableExpr(scope *table.Scope, expr ast.Expr) bool {
	if c == nil || scope == nil || expr == nil {
		return false
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return scope.IsMutableVar(e.Name)
	case *ast.SelectorExpr:
		baseType := c.typeExpr(scope, e.Expr, nil)
		if _, ok := baseType.(*typeinfo.RawPtrType); ok {
			return true
		}
		return c.isMutableAddressableExpr(scope, e.Expr)
	}
	return false
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
		return expr, "pointer receiver method requires a mutable binding; `" + ident.Name + "` is const", true
	case symbols.SymbolVar:
		if !sym.IsMutable() {
			return expr, "pointer receiver method requires a mutable binding; `" + ident.Name + "` is immutable", true
		}
	}
	return nil, "", false
}

func (c *checker) checkMethodCall(scope *table.Scope, receiverExpr ast.Expr, callExpr *ast.CallExpr, calleeType typeinfo.Type, args []typeinfo.Type, _ *symbols.Symbol) {
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
			if ptrType, ok := paramType.(*typeinfo.RawPtrType); ok && ptrType != nil && c.matchesPointerReceiverTarget(ptrType.Target, argType) {
				if c.isMutableAddressableExpr(scope, receiverExpr) {
					continue
				}
				if site, msg, ok := c.mutableReceiverDiagnostic(scope, receiverExpr); ok {
					c.ctx.Diagnostics.AddError(diagnostics.ErrInvalidAssignment, msg, ast.LocOf(site), "immutable binding defined here")
					continue
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
		}
	}
}

func (c *checker) matchesPointerReceiverTarget(target, arg typeinfo.Type) bool {
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
	resolved, ok := project.LookupImportedSymbol(c.ctx, c.module, node.Module.Name, node.Name.Name)
	if !ok || resolved.Symbol == nil {
		return &typeinfo.InvalidType{}
	}
	t, ok := symbols.GetSymbolType(resolved.Symbol)
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
