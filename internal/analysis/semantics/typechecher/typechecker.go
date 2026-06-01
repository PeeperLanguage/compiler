package typechecher

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

// isInvalidOrUnknown replaces the repeated `typeinfo.IsInvalid(t) || typeinfo.IsUnknown(t)` pattern.
func isInvalidOrUnknown(t typeinfo.Type) bool {
	return typeinfo.IsInvalid(t) || typeinfo.IsUnknown(t)
}

func isAllowedType(t typeinfo.Type) bool {
	if typeinfo.IsArithmetic(t) {
		return true
	}
	fn, ok := t.(*typeinfo.FuncType)
	if !ok || fn == nil {
		return false
	}
	for _, param := range fn.Params {
		if !isAllowedType(param) {
			return false
		}
	}
	return isAllowedType(fn.Return)
}

func (c *checker) typeFromSyntax(node ast.TypeExpr) typeinfo.Type {
	typ := typeinfo.TypeFromSyntax(node)
	named, ok := typ.(*typeinfo.NamedType)
	if !ok || named == nil || named.Name == "" || c == nil || c.module == nil || c.module.ModuleScope == nil {
		return typ
	}
	sym, found := c.module.ModuleScope.Lookup(named.Name)
	if !found || sym == nil || sym.Kind != symbols.SymbolType {
		return typ
	}
	resolved, ok := lookupSymbolType(sym)
	if !ok {
		return typ
	}
	return resolved
}

func (c *checker) fnTypeFromDecl(decl *ast.FnDecl) *typeinfo.FuncType {
	if decl == nil {
		return nil
	}
	params := make([]typeinfo.Type, 0, len(decl.Params))
	for _, param := range decl.Params {
		params = append(params, c.typeFromSyntax(param.Type))
	}
	return &typeinfo.FuncType{
		Params: params,
		Return: c.typeFromSyntax(decl.ReturnType),
	}
}

func bindSymbolType(sym *symbols.Symbol, typ typeinfo.Type) {
	if sym == nil || typ == nil {
		return
	}
	sym.Type = typ
}

func lookupSymbolType(sym *symbols.Symbol) (typeinfo.Type, bool) {
	if sym == nil || sym.Type == nil {
		return nil, false
	}
	typ, ok := sym.Type.(typeinfo.Type)
	return typ, ok && typ != nil
}

// -----------------------------------------------------------------------------

func (c *checker) checkModule() {
	if c == nil || c.module == nil || c.module.AST == nil {
		return
	}
	for _, decl := range c.module.AST.Decls {
		if fn, ok := decl.(*ast.FnDecl); ok && fn != nil {
			sym, found := c.module.ModuleScope.Lookup(fn.Name.Name)
			if !found || sym == nil {
				continue
			}
			if fn.Body == nil {
				bindSymbolType(sym, c.fnTypeFromDecl(fn))
			} else {
				c.checkFunction(sym, fn)
			}
		}
	}
}

func (c *checker) checkFunction(sym *symbols.Symbol, fn *ast.FnDecl) {
	if c == nil || sym == nil || fn == nil || fn.Body == nil {
		return
	}
	c.checkFunctionShape(fn)
	bindSymbolType(sym, c.fnTypeFromDecl(fn))
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
		bindSymbolType(paramSym, c.typeFromSyntax(param.Type))
	}
	c.checkBlock(funcScope, fn.Body, c.typeFromSyntax(fn.ReturnType))
}

func (c *checker) checkBlock(parentScope *table.Scope, block *ast.BlockStmt, returnType typeinfo.Type) {
	if block == nil {
		return
	}
	scope := parentScope
	if s, ok := c.module.BlockScopes[block]; ok && s != nil {
		scope = s
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
		if !typeinfo.Assignable(returnType, retType) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.Value, diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot return %s from function returning %s",
					typeinfo.TypeText(retType), typeinfo.TypeText(returnType)))
		}
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
		} else {
			condType := c.typeExpr(scope, node.Cond, nil)
			if condType != nil && !isInvalidOrUnknown(condType) && !typeinfo.IsCondition(condType) {
				common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.Cond, diagnostics.ErrInvalidOperation,
					"if condition must be bool or scalar number")
			}
		}
		c.checkBlock(scope, node.Then, returnType)
		c.checkStmt(scope, node.Else, returnType)
	case *ast.ExprStmt:
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidStatement,
			"expression statements unsupported in current compiler stage")
	default:
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidStatement,
			"unsupported statement for arithmetic flow")
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
			bindSymbolType(sym, &typeinfo.InvalidType{})
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrMissingInitializer,
				"missing initializer for const declaration")
			return
		}
		if declType == nil {
			bindSymbolType(sym, &typeinfo.InvalidType{})
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrMissingType,
				"let declaration needs type or initializer")
			return
		}
		bindSymbolType(sym, declType)
		return
	}

	valType := c.typeExpr(scope, value, declType)
	if valType == nil {
		return
	}
	if declType != nil && !typeinfo.Assignable(declType, valType) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, value, diagnostics.ErrTypeMismatch,
			fmt.Sprintf("cannot assign %s to %s",
				typeinfo.TypeText(valType), typeinfo.TypeText(declType)))
		bindSymbolType(sym, &typeinfo.InvalidType{})
		return
	}
	if declType != nil {
		bindSymbolType(sym, declType)
	} else {
		bindSymbolType(sym, valType)
	}
}

func (c *checker) checkFunctionShape(decl *ast.FnDecl) {
	if decl == nil {
		return
	}
	if decl.Receiver != nil {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, decl, diagnostics.ErrInvalidMethodReceiver,
			"receivers not supported in current compiler stage")
		return
	}
	if !isAllowedType(c.typeFromSyntax(decl.ReturnType)) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, decl, diagnostics.ErrInvalidReturn,
			"function return type must be builtin integer, f32/f64, or function type in current compiler stage")
		return
	}
	for _, param := range decl.Params {
		if !isAllowedType(c.typeFromSyntax(param.Type)) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, param.Name, diagnostics.ErrInvalidType,
				"parameter type must be builtin integer, f32/f64, or function type in current compiler stage")
			return
		}
	}
}

// typeExpr computes the type of an expression using scope lookup and returns it.
// The typechecker no longer builds a parallel typeinfo.Expr tree — types are
// stored directly on symbols. HIR-lower will re-derive types during lowering.
func (c *checker) typeExpr(scope *table.Scope, expr ast.Expr, expected typeinfo.Type) typeinfo.Type {
	if expr == nil {
		return nil
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		return c.typeNumber(node, expected)

	case *ast.Ident:
		sym, ok := scope.Lookup(node.Name)
		if !ok || sym == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrUnknownIdentifier,
				fmt.Sprintf("unknown identifier `%s`\n", node.Name))
			return &typeinfo.InvalidType{}
		}
		t, ok := lookupSymbolType(sym)
		if !ok || t == nil {
			return &typeinfo.UnknownType{}
		}
		return t

	case *ast.ScopeResolution:
		return c.qualifiedScopeType(scope, node)

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
			"unsupported expression for arithmetic flow")
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
	if isInvalidOrUnknown(argType) {
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

	if left == nil || right == nil || isInvalidOrUnknown(left) || isInvalidOrUnknown(right) {
		return &typeinfo.InvalidType{}
	}

	commonType := typeinfo.CommonNumericType(left, right)
	if commonType == nil && !typeinfo.Assignable(left, right) && !typeinfo.Assignable(right, left) {
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
	calleeType := c.typeExpr(scope, node.Callee, expected)
	argTypes := make([]typeinfo.Type, 0, len(node.Args))
	for _, arg := range node.Args {
		argTypes = append(argTypes, c.typeExpr(scope, arg, nil))
	}
	c.checkFunctionCall(node, calleeType, argTypes)
	return c.callReturnType(node, calleeType)
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
			if call != nil && !isInvalidOrUnknown(fnType.Return) {
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
	if targetType == nil || isInvalidOrUnknown(targetType) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.TypeExpr, diagnostics.ErrInvalidType,
			"invalid target type for cast")
		return &typeinfo.InvalidType{}
	}
	if node.Expr == nil {
		return &typeinfo.InvalidType{}
	}
	exprType := c.typeExpr(scope, node.Expr, nil)
	if exprType == nil || isInvalidOrUnknown(exprType) {
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
	if isInvalidOrUnknown(expected) {
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
		if !isInvalidOrUnknown(calleeType) {
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
		if !typeinfo.Assignable(paramType, argType) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, callExpr.Args[i], diagnostics.ErrTypeMismatch,
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
	t, ok := lookupSymbolType(sym)
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
