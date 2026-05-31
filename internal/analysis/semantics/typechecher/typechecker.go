package typechecher

import (
	"fmt"
	"strings"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/symbols"
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

// invalidAs builds the sentinel As node used in every early-return inside typeAsExpr.
func (c *checker) invalidAs(expr typeinfo.Expr, castType typeinfo.Type) *typeinfo.As {
	return &typeinfo.As{Expr: expr, CastType: castType, ExprType: &typeinfo.InvalidType{}}
}

// -----------------------------------------------------------------------------

func (c *checker) checkModule() {
	if c == nil || c.module == nil {
		return
	}
	c.module.ResetTypedExprs()
	for _, ex := range c.module.Externs {
		if ex.Symbol == nil || ex.Decl == nil {
			continue
		}
		bindSymbolType(ex.Symbol, c.fnTypeFromDecl(ex.Decl))
	}
	for _, declFn := range c.module.Functions {
		if declFn == nil || declFn.Decl == nil || declFn.Scope == nil {
			continue
		}
		c.checkFunction(declFn)
	}
}

func (c *checker) checkFunction(fn *declinfo.Function) {
	if c == nil || fn == nil || fn.Decl == nil {
		return
	}
	decl := fn.Decl
	c.checkFunctionShape(decl)
	if fn.Symbol == nil {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, decl, diagnostics.ErrUndefinedSymbol, "missing function binding")
		return
	}
	bindSymbolType(fn.Symbol, c.fnTypeFromDecl(decl))
	for _, param := range decl.Params {
		if param.Name == nil {
			continue
		}
		res, ok := c.module.LookupResolution(param.Name)
		if !ok || res == nil || res.Symbol == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, decl, diagnostics.ErrUndefinedSymbol, "missing parameter binding")
			return
		}
		bindSymbolType(res.Symbol, c.typeFromSyntax(param.Type))
	}
	c.checkBlock(decl, decl.Body, c.typeFromSyntax(decl.ReturnType))
}

func (c *checker) checkBlock(fn *ast.FnDecl, block *ast.BlockStmt, returnType typeinfo.Type) {
	if block == nil {
		return
	}
	for _, stmt := range block.Stmts {
		c.checkStmt(fn, stmt, returnType)
	}
}

func (c *checker) checkStmt(fn *ast.FnDecl, stmt ast.Stmt, returnType typeinfo.Type) {
	if stmt == nil {
		return
	}
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		c.checkBlock(fn, node, returnType)
	case *ast.LetDecl:
		c.checkBinding(node, false)
	case *ast.ConstDecl:
		c.checkBinding(node, true)
	case *ast.ReturnStmt:
		if node.Value == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return
		}
		typedExpr := c.typeExpr(node.Value, returnType)
		if typedExpr == nil {
			return
		}
		if !typeinfo.Assignable(returnType, typedExpr.Type()) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.Value, diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot return %s from function returning %s",
					typeinfo.TypeText(typedExpr.Type()), typeinfo.TypeText(returnType)))
			return
		}
		c.module.BindTypedExpr(node.Value, typedExpr)
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
		} else {
			cond := c.typeExpr(node.Cond, nil)
			if cond == nil {
				return
			}
			c.module.BindTypedExpr(node.Cond, cond)
			if !isInvalidOrUnknown(cond.Type()) && !typeinfo.IsCondition(cond.Type()) {
				common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.Cond, diagnostics.ErrInvalidOperation,
					"if condition must be bool or scalar number")
			}
		}
		c.checkBlock(fn, node.Then, returnType)
		c.checkStmt(fn, node.Else, returnType)
	case *ast.ExprStmt:
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidStatement,
			"expression statements unsupported in current compiler stage")
	default:
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidStatement,
			"unsupported statement for arithmetic flow")
	}
}

func (c *checker) checkBinding(node ast.Stmt, requireInitializer bool) {
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

	bindRes, found := c.module.LookupResolution(nameNode)
	if !found || bindRes == nil || bindRes.Symbol == nil {
		return
	}

	if value == nil {
		if requireInitializer {
			bindSymbolType(bindRes.Symbol, &typeinfo.InvalidType{})
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrMissingInitializer,
				"missing initializer for const declaration")
			return
		}
		if declType == nil {
			bindSymbolType(bindRes.Symbol, &typeinfo.InvalidType{})
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrMissingType,
				"let declaration needs type or initializer")
			return
		}
		bindSymbolType(bindRes.Symbol, declType)
		return
	}

	typedExpr := c.typeExpr(value, declType)
	if typedExpr == nil {
		return
	}
	if declType != nil && !typeinfo.Assignable(declType, typedExpr.Type()) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, value, diagnostics.ErrTypeMismatch,
			fmt.Sprintf("cannot assign %s to %s",
				typeinfo.TypeText(typedExpr.Type()), typeinfo.TypeText(declType)))
		bindSymbolType(bindRes.Symbol, &typeinfo.InvalidType{})
		return
	}
	c.module.BindTypedExpr(value, typedExpr)
	if declType != nil {
		bindSymbolType(bindRes.Symbol, declType)
	} else {
		bindSymbolType(bindRes.Symbol, typedExpr.Type())
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

func (c *checker) typeExpr(expr ast.Expr, expected typeinfo.Type) typeinfo.Expr {
	if expr == nil {
		return nil
	}
	switch node := expr.(type) {
	case *ast.NumberLit:
		typedExpr := c.typeNumber(node, expected)
		c.module.BindTypedExpr(node, typedExpr)
		return typedExpr

	case *ast.Ident:
		resolution, ok := c.module.LookupResolution(node)
		if !ok || resolution == nil || resolution.Symbol == nil {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrUnknownIdentifier,
				fmt.Sprintf("unknown identifier `%s`\n", node.Name))
			expr := &typeinfo.Ident{Symbol: nil, ExprType: &typeinfo.InvalidType{}}
			c.module.BindTypedExpr(node, expr)
			return expr
		}
		exprType, ok := lookupSymbolType(resolution.Symbol)
		if !ok || exprType == nil {
			exprType = &typeinfo.UnknownType{}
		}
		expr := &typeinfo.Ident{Symbol: resolution.Symbol, ExprType: exprType}
		c.module.BindTypedExpr(node, expr)
		return expr

	case *ast.UnaryExpr:
		return c.typeUnaryExpr(node, expected)

	case *ast.BinaryExpr:
		return c.typeBinaryExpr(node, expected)

	case *ast.CallExpr:
		return c.typeCallExpr(node, expected)

	case *ast.AsExpr:
		return c.typeAsExpr(node)

	default:
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidExpression,
			"unsupported expression for arithmetic flow")
		return nil
	}
}

// typeUnaryExpr handles unary expression type-checking, extracted from the
// large typeExpr switch to keep each case manageable.
func (c *checker) typeUnaryExpr(node *ast.UnaryExpr, expected typeinfo.Type) typeinfo.Expr {
	if node.Op != "+" && node.Op != "-" && node.Op != "!" {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported unary operator `"+node.Op+"`")
		return nil
	}

	argExpected := typeinfo.Type(nil)
	if node.Op != "!" && expected != nil && typeinfo.IsArithmetic(expected) {
		argExpected = expected
		// `-128` fits i8, but raw literal `128` does not – type operand with
		// default size first, then apply signed-fit rules below.
		if node.Op == "-" {
			if _, ok := expected.(*typeinfo.IntegerType); ok {
				if _, ok := node.Expr.(*ast.NumberLit); ok {
					argExpected = nil
				}
			}
		}
	}

	arg := c.typeExpr(node.Expr, argExpected)
	if arg == nil {
		expr := &typeinfo.Unary{Op: node.Op, Arg: nil, ExprType: &typeinfo.InvalidType{}}
		c.module.BindTypedExpr(node, expr)
		return expr
	}

	if expected == nil && node.Op == "-" {
		if literal, ok := node.Expr.(*ast.NumberLit); ok && !numeric.IsFloat(literal.Value) {
			typed := &typeinfo.IntLit{
				Value:    literal.Value,
				ExprType: typeinfo.DefaultNumberType(signedLiteralText(node.Op, literal.Value)),
			}
			c.module.BindTypedExpr(literal, typed)
			arg = typed
		}
	}

	if expected != nil && node.Op != "!" && typeinfo.SameType(arg.Type(), typeinfo.DefaultIntegerType()) {
		if c.signedNumberFits(node.Op, node.Expr, expected) {
			if literal, ok := node.Expr.(*ast.NumberLit); ok && !numeric.IsFloat(literal.Value) {
				typed := &typeinfo.IntLit{Value: literal.Value, ExprType: expected}
				c.module.BindTypedExpr(literal, typed)
				arg = typed
			}
		} else if literal, ok := node.Expr.(*ast.NumberLit); ok && node.Op == "-" {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, literal, diagnostics.ErrInvalidNumber,
				fmt.Sprintf("literal `%s` does not fit %s",
					signedLiteralText(node.Op, literal.Value), typeinfo.TypeText(expected)))
			return nil
		}
	}

	if isInvalidOrUnknown(arg.Type()) {
		expr := &typeinfo.Unary{Op: node.Op, Arg: arg, ExprType: &typeinfo.InvalidType{}}
		c.module.BindTypedExpr(node, expr)
		return expr
	}

	if !typeinfo.IsArithmetic(arg.Type()) && !typeinfo.SameType(arg.Type(), &typeinfo.BoolType{}) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported unary operand type")
		return nil
	}

	exprType := arg.Type()
	if node.Op == "!" {
		exprType = &typeinfo.BoolType{}
	}
	expr := &typeinfo.Unary{Op: node.Op, Arg: arg, ExprType: exprType}
	c.module.BindTypedExpr(node, expr)
	return expr
}

// typeBinaryExpr handles binary expression type-checking.
func (c *checker) typeBinaryExpr(node *ast.BinaryExpr, expected typeinfo.Type) typeinfo.Expr {
	if !c.allowedOp(node.Op) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported binary operator `"+node.Op+"`")
		return nil
	}

	left := c.typeExpr(node.Left, expected)
	right := c.typeExpr(node.Right, expected)

	// Build the sentinel early if either side is bad.
	if left == nil || right == nil || isInvalidOrUnknown(left.Type()) || isInvalidOrUnknown(right.Type()) {
		expr := &typeinfo.Binary{Op: node.Op, Left: left, Right: right, ExprType: &typeinfo.InvalidType{}}
		c.module.BindTypedExpr(node, expr)
		return expr
	}

	commonType := typeinfo.CommonNumericType(left.Type(), right.Type())
	if commonType == nil && !typeinfo.Assignable(left.Type(), right.Type()) && !typeinfo.Assignable(right.Type(), left.Type()) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrTypeMismatch,
			fmt.Sprintf("operand types mismatch: %s vs %s",
				typeinfo.TypeText(left.Type()), typeinfo.TypeText(right.Type())))
		return nil
	}

	exprType := left.Type()
	if commonType != nil {
		exprType = commonType
	}
	switch node.Op {
	case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
		exprType = &typeinfo.BoolType{}
	}

	if !c.validBinaryTypes(node.Op, left.Type()) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported operand type for operator `"+node.Op+"`")
		return nil
	}

	expr := &typeinfo.Binary{Op: node.Op, Left: left, Right: right, ExprType: exprType}
	c.module.BindTypedExpr(node, expr)
	return expr
}

// typeCallExpr handles call expression type-checking.
func (c *checker) typeCallExpr(node *ast.CallExpr, expected typeinfo.Type) typeinfo.Expr {
	callee := c.typeExpr(node.Callee, expected)
	args := make([]typeinfo.Expr, 0, len(node.Args))
	for _, arg := range node.Args {
		args = append(args, c.typeExpr(arg, nil))
	}
	c.checkFunctionCall(node, callee, args)

	returnType := c.callReturnType(node, callee)
	expr := &typeinfo.Call{Callee: callee, Args: args, ExprType: returnType}
	c.module.BindTypedExpr(node, expr)
	return expr
}

func (c *checker) callReturnType(call *ast.CallExpr, callee typeinfo.Expr) typeinfo.Type {
	if c == nil {
		return &typeinfo.InvalidType{}
	}
	if callee != nil {
		if fnType, ok := callee.Type().(*typeinfo.FuncType); ok && fnType != nil {
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

func (c *checker) typeAsExpr(node *ast.AsExpr) typeinfo.Expr {
	if c == nil || node == nil {
		return nil
	}

	targetType := c.typeFromSyntax(node.TypeExpr)
	if targetType == nil || isInvalidOrUnknown(targetType) {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node.TypeExpr, diagnostics.ErrInvalidType,
			"invalid target type for cast")
		return c.invalidAs(nil, targetType)
	}

	if node.Expr == nil {
		return c.invalidAs(nil, targetType)
	}

	expr := c.typeExpr(node.Expr, nil)
	if expr == nil || isInvalidOrUnknown(expr.Type()) {
		return c.invalidAs(expr, targetType)
	}

	compat := typeinfo.CheckNumericCompatibility(targetType, expr.Type())
	if compat == typeinfo.Incompatible {
		common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidCast,
			fmt.Sprintf("cannot cast %s to %s",
				typeinfo.TypeText(expr.Type()), typeinfo.TypeText(targetType)))
		return c.invalidAs(expr, targetType)
	}

	return &typeinfo.As{Expr: expr, CastType: targetType, ExprType: targetType}
}

func (c *checker) typeNumber(node *ast.NumberLit, expected typeinfo.Type) typeinfo.Expr {
	if node == nil {
		return nil
	}
	if isInvalidOrUnknown(expected) {
		expected = nil
	}
	if expected != nil {
		// Type-level gate: e.g. float literal → integer type is Incompatible.
		naturalType := typeinfo.DefaultNumberType(node.Value)
		if typeinfo.CheckNumericCompatibility(expected, naturalType) == typeinfo.Incompatible {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrTypeMismatch,
				fmt.Sprintf("literal `%s` cannot be used as %s", node.Value, typeinfo.TypeText(expected)))
			return nil
		}
		// Value-level gate: literal must actually fit the target type's range.
		if !literalFitsType(node.Value, expected) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, node, diagnostics.ErrInvalidNumber,
				fmt.Sprintf("literal `%s` does not fit %s", node.Value, typeinfo.TypeText(expected)))
			return nil
		}
		return buildNumberLiteral(node.Value, expected)
	}
	if numeric.IsFloat(node.Value) {
		return &typeinfo.FloatLit{Value: node.Value, ExprType: typeinfo.DefaultNumberType(node.Value)}
	}
	return &typeinfo.IntLit{Value: node.Value, ExprType: typeinfo.DefaultNumberType(node.Value)}
}

// literalFitsType checks whether the raw literal value fits inside the target
// numeric type's range. It is purely value-level; type-level compatibility
// should already be confirmed before calling this.
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

// buildNumberLiteral constructs the correctly-typed literal node for a given
// target type. Caller must ensure the value fits before calling.
func buildNumberLiteral(value string, typ typeinfo.Type) typeinfo.Expr {
	switch t := typ.(type) {
	case *typeinfo.IntegerType:
		return &typeinfo.IntLit{Value: value, ExprType: t}
	case *typeinfo.FloatType:
		return &typeinfo.FloatLit{Value: floatLiteralValue(value), ExprType: t}
	}
	return nil
}

func (c *checker) signedNumberFits(op string, expr ast.Expr, target typeinfo.Type) bool {
	if op != "+" && op != "-" {
		return false
	}
	literal, ok := expr.(*ast.NumberLit)
	if !ok || target == nil {
		return false
	}
	value := signedLiteralText(op, literal.Value)
	switch typ := target.(type) {
	case *typeinfo.IntegerType:
		return !numeric.IsFloat(value) && numeric.FitsIntegerLiteral(value, typ.Bits, typ.Signed)
	case *typeinfo.FloatType:
		if numeric.IsFloat(value) {
			return numeric.FitsFloatLiteral(value, typ.Bits)
		}
		return numeric.FitsIntegerLiteralInFloat(value, typ.Bits)
	default:
		return false
	}
}

func signedLiteralText(op, value string) string {
	if op == "-" && !strings.HasPrefix(value, "-") {
		return "-" + value
	}
	return value
}

func floatLiteralValue(value string) string {
	if numeric.IsFloat(value) {
		return value
	}
	intValue, err := numeric.StringToBigInt(value)
	if err != nil {
		return value
	}
	return intValue.String() + ".0"
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

func (c *checker) checkFunctionCall(callExpr *ast.CallExpr, callee typeinfo.Expr, args []typeinfo.Expr) {
	if c == nil || callExpr == nil || callee == nil {
		return
	}

	calleeType := callee.Type()
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

	for i, arg := range args {
		if arg == nil {
			continue
		}
		paramType := fnType.Params[i]
		if paramType == nil {
			continue
		}
		if !typeinfo.Assignable(paramType, arg.Type()) {
			common.AddError(c.ctx.Diagnostics, c.module.FilePath, callExpr.Args[i], diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot implicitly convert %s to %s",
					typeinfo.TypeText(arg.Type()), typeinfo.TypeText(paramType)))
		}
	}
}

func Check(ctx *context.CompilerContext, module *context.Module) {
	if module == nil || ctx == nil {
		return
	}
	(&checker{ctx: ctx, module: module}).checkModule()
}
