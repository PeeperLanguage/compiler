package typechecher

import (
	"fmt"
	"strings"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/utils/numeric"
)

type checker struct {
	module *context.Module
	diag   *diagnostics.DiagnosticBag
}

// --- helpers -----------------------------------------------------------------

// isInvalidOrUnknown replaces the repeated `typeinfo.IsInvalid(t) || typeinfo.IsUnknown(t)` pattern.
func isInvalidOrUnknown(t typeinfo.Type) bool {
	return typeinfo.IsInvalid(t) || typeinfo.IsUnknown(t)
}

// invalidAs builds the sentinel As node used in every early-return inside typeAsExpr.
func (c *checker) invalidAs(expr typeinfo.Expr, castType typeinfo.Type) *typeinfo.As {
	return &typeinfo.As{Expr: expr, CastType: castType, ExprType: &typeinfo.InvalidType{}}
}

// -----------------------------------------------------------------------------

func (c *checker) checkModule() {
	if c == nil || c.module == nil || c.module.Decls == nil || c.module.Bindings == nil {
		return
	}
	c.module.Types = typeinfo.NewModuleInfo()
	c.module.Types.Externs = append(c.module.Types.Externs, c.module.Decls.Externs...)
	for _, declFn := range c.module.Decls.Functions {
		if declFn == nil || declFn.Decl == nil || declFn.Scope == nil {
			continue
		}
		c.checkFunction(declFn.Decl)
	}
}

func (c *checker) checkFunction(decl *ast.FnDecl) {
	if c == nil || decl == nil {
		return
	}
	c.checkFunctionShape(decl)
	sym, ok := c.module.Bindings.FunctionSymbols[decl]
	if !ok || sym == nil {
		common.AddError(c.diag, c.module.FilePath, decl, diagnostics.ErrUndefinedSymbol, "missing function binding")
		return
	}
	c.module.Types.BindSymbolType(sym, typeinfo.TypeFromSyntax(decl.ReturnType))
	for _, param := range decl.Params {
		if param.Name == nil {
			continue
		}
		res, ok := c.module.Bindings.LookupNode(param.Name)
		if !ok || res == nil || res.Symbol == nil {
			common.AddError(c.diag, c.module.FilePath, decl, diagnostics.ErrUndefinedSymbol, "missing parameter binding")
			return
		}
		c.module.Types.BindSymbolType(res.Symbol, typeinfo.TypeFromSyntax(param.Type))
	}
	c.checkBlock(decl, decl.Body, typeinfo.TypeFromSyntax(decl.ReturnType))
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
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return
		}
		typedExpr := c.typeExpr(node.Value, returnType)
		if typedExpr == nil {
			return
		}
		if !typeinfo.Assignable(returnType, typedExpr.Type()) {
			common.AddError(c.diag, c.module.FilePath, node.Value, diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot return %s from function returning %s",
					typeinfo.TypeText(typedExpr.Type()), typeinfo.TypeText(returnType)))
			return
		}
		c.module.Types.BindExpr(node.Value, typedExpr)
		c.module.Types.BindFunctionReturn(fn, typedExpr.Type())
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
		} else {
			cond := c.typeExpr(node.Cond, nil)
			if cond == nil {
				return
			}
			c.module.Types.BindExpr(node.Cond, cond)
			if !isInvalidOrUnknown(cond.Type()) && !c.isConditionType(cond.Type()) {
				common.AddError(c.diag, c.module.FilePath, node.Cond, diagnostics.ErrInvalidOperation,
					"if condition must be bool or scalar number")
			}
		}
		c.checkBlock(fn, node.Then, returnType)
		c.checkStmt(fn, node.Else, returnType)
	case *ast.ExprStmt:
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidStatement,
			"expression statements unsupported in current compiler stage")
	default:
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidStatement,
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
		declType = typeinfo.TypeFromSyntax(bind.Type)
		value = bind.Value
	case *ast.ConstDecl:
		nameNode = bind.Name
		declType = typeinfo.TypeFromSyntax(bind.Type)
		value = bind.Value
	default:
		return
	}

	bindRes, ok := c.module.Bindings.LookupNode(nameNode)
	if !ok || bindRes == nil || bindRes.Symbol == nil {
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrUndefinedSymbol, "missing binding symbol")
		return
	}

	if value == nil {
		if requireInitializer {
			c.module.Types.BindSymbolType(bindRes.Symbol, &typeinfo.InvalidType{})
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrMissingInitializer,
				"missing initializer for const declaration")
			return
		}
		if declType == nil {
			c.module.Types.BindSymbolType(bindRes.Symbol, &typeinfo.InvalidType{})
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrMissingType,
				"let declaration needs type or initializer")
			return
		}
		c.module.Types.BindSymbolType(bindRes.Symbol, declType)
		return
	}

	typedExpr := c.typeExpr(value, declType)
	if typedExpr == nil {
		return
	}
	if declType != nil && !typeinfo.Assignable(declType, typedExpr.Type()) {
		common.AddError(c.diag, c.module.FilePath, value, diagnostics.ErrTypeMismatch,
			fmt.Sprintf("cannot assign %s to %s",
				typeinfo.TypeText(typedExpr.Type()), typeinfo.TypeText(declType)))
		c.module.Types.BindSymbolType(bindRes.Symbol, &typeinfo.InvalidType{})
		return
	}
	c.module.Types.BindExpr(value, typedExpr)
	if declType != nil {
		c.module.Types.BindSymbolType(bindRes.Symbol, declType)
	} else {
		c.module.Types.BindSymbolType(bindRes.Symbol, typedExpr.Type())
	}
}

func (c *checker) checkFunctionShape(decl *ast.FnDecl) {
	if decl == nil {
		return
	}
	if decl.Receiver != nil {
		common.AddError(c.diag, c.module.FilePath, decl, diagnostics.ErrInvalidMethodReceiver,
			"receivers not supported in current compiler stage")
		return
	}
	if !c.isSupportedScalarType(typeinfo.TypeFromSyntax(decl.ReturnType)) {
		common.AddError(c.diag, c.module.FilePath, decl, diagnostics.ErrInvalidReturn,
			"function return type must be builtin integer or f32/f64 in current compiler stage")
		return
	}
	for _, param := range decl.Params {
		if !c.isSupportedScalarType(typeinfo.TypeFromSyntax(param.Type)) {
			common.AddError(c.diag, c.module.FilePath, param.Name, diagnostics.ErrInvalidType,
				"parameter type must be builtin integer or f32/f64 in current compiler stage")
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
		c.module.Types.BindExpr(node, typedExpr)
		return typedExpr

	case *ast.Ident:
		resolution, ok := c.module.Bindings.LookupNode(node)
		if !ok || resolution == nil || resolution.Symbol == nil {
			expr := &typeinfo.Ident{Symbol: nil, ExprType: &typeinfo.InvalidType{}}
			c.module.Types.BindExpr(node, expr)
			return expr
		}
		exprType, ok := c.module.Types.LookupSymbolType(resolution.Symbol)
		if !ok || exprType == nil {
			exprType = &typeinfo.UnknownType{}
		}
		expr := &typeinfo.Ident{Symbol: resolution.Symbol, ExprType: exprType}
		c.module.Types.BindExpr(node, expr)
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
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidExpression,
			"unsupported expression for arithmetic flow")
		return nil
	}
}

// typeUnaryExpr handles unary expression type-checking, extracted from the
// large typeExpr switch to keep each case manageable.
func (c *checker) typeUnaryExpr(node *ast.UnaryExpr, expected typeinfo.Type) typeinfo.Expr {
	if node.Op != "+" && node.Op != "-" && node.Op != "!" {
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
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
		c.module.Types.BindExpr(node, expr)
		return expr
	}

	if expected == nil && node.Op == "-" {
		if literal, ok := node.Expr.(*ast.NumberLit); ok && !numeric.IsFloat(literal.Value) {
			typed := &typeinfo.IntLit{
				Value:    literal.Value,
				ExprType: typeinfo.DefaultNumberType(signedLiteralText(node.Op, literal.Value)),
			}
			c.module.Types.BindExpr(literal, typed)
			arg = typed
		}
	}

	if expected != nil && node.Op != "!" && typeinfo.SameType(arg.Type(), typeinfo.DefaultIntegerType()) {
		if c.signedNumberFits(node.Op, node.Expr, expected) {
			if literal, ok := node.Expr.(*ast.NumberLit); ok && !numeric.IsFloat(literal.Value) {
				typed := &typeinfo.IntLit{Value: literal.Value, ExprType: expected}
				c.module.Types.BindExpr(literal, typed)
				arg = typed
			}
		} else if literal, ok := node.Expr.(*ast.NumberLit); ok && node.Op == "-" {
			common.AddError(c.diag, c.module.FilePath, literal, diagnostics.ErrInvalidNumber,
				fmt.Sprintf("literal `%s` does not fit %s",
					signedLiteralText(node.Op, literal.Value), typeinfo.TypeText(expected)))
			return nil
		}
	}

	if isInvalidOrUnknown(arg.Type()) {
		expr := &typeinfo.Unary{Op: node.Op, Arg: arg, ExprType: &typeinfo.InvalidType{}}
		c.module.Types.BindExpr(node, expr)
		return expr
	}

	if !typeinfo.IsArithmetic(arg.Type()) && !typeinfo.SameType(arg.Type(), &typeinfo.BoolType{}) {
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported unary operand type")
		return nil
	}

	exprType := arg.Type()
	if node.Op == "!" {
		exprType = &typeinfo.BoolType{}
	}
	expr := &typeinfo.Unary{Op: node.Op, Arg: arg, ExprType: exprType}
	c.module.Types.BindExpr(node, expr)
	return expr
}

// typeBinaryExpr handles binary expression type-checking.
func (c *checker) typeBinaryExpr(node *ast.BinaryExpr, expected typeinfo.Type) typeinfo.Expr {
	if !c.allowedOp(node.Op) {
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported binary operator `"+node.Op+"`")
		return nil
	}

	left := c.typeExpr(node.Left, expected)
	right := c.typeExpr(node.Right, expected)

	// Build the sentinel early if either side is bad.
	if left == nil || right == nil || isInvalidOrUnknown(left.Type()) || isInvalidOrUnknown(right.Type()) {
		expr := &typeinfo.Binary{Op: node.Op, Left: left, Right: right, ExprType: &typeinfo.InvalidType{}}
		c.module.Types.BindExpr(node, expr)
		return expr
	}

	commonType := typeinfo.CommonNumericType(left.Type(), right.Type())
	if commonType == nil && !typeinfo.Assignable(left.Type(), right.Type()) && !typeinfo.Assignable(right.Type(), left.Type()) {
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrTypeMismatch,
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
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidOperation,
			"unsupported operand type for operator `"+node.Op+"`")
		return nil
	}

	expr := &typeinfo.Binary{Op: node.Op, Left: left, Right: right, ExprType: exprType}
	c.module.Types.BindExpr(node, expr)
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

	exprType := typeinfo.Type(&typeinfo.InvalidType{})
	if callee != nil {
		exprType = callee.Type()
	}
	expr := &typeinfo.Call{Callee: callee, Args: args, ExprType: exprType}
	c.module.Types.BindExpr(node, expr)
	return expr
}

func (c *checker) typeAsExpr(node *ast.AsExpr) typeinfo.Expr {
	if c == nil || node == nil {
		return nil
	}

	targetType := typeinfo.TypeFromSyntax(node.TypeExpr)
	if targetType == nil || isInvalidOrUnknown(targetType) {
		common.AddError(c.diag, c.module.FilePath, node.TypeExpr, diagnostics.ErrInvalidType,
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
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidCast,
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
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrTypeMismatch,
				fmt.Sprintf("literal `%s` cannot be used as %s", node.Value, typeinfo.TypeText(expected)))
			return nil
		}
		// Value-level gate: literal must actually fit the target type's range.
		if !literalFitsType(node.Value, expected) {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidNumber,
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

func (c *checker) isSupportedScalarType(typ typeinfo.Type) bool { return typeinfo.IsArithmetic(typ) }
func (c *checker) isConditionType(typ typeinfo.Type) bool       { return typeinfo.IsCondition(typ) }

func (c *checker) validBinaryTypes(op string, typ typeinfo.Type) bool {
	switch op {
	case "+", "-", "*", "/":
		return typeinfo.IsArithmetic(typ)
	case "%":
		return typeinfo.IsIntegral(typ)
	case "==", "!=":
		return typeinfo.IsEquatable(typ)
	case "<", "<=", ">", ">=":
		return typeinfo.IsOrderable(typ)
	case "&&", "||":
		return typeinfo.IsLogical(typ)
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

	ident, ok := callee.(*typeinfo.Ident)
	if !ok || ident == nil || ident.Symbol == nil {
		return
	}

	var fnDecl *ast.FnDecl
	for declFn, sym := range c.module.Bindings.FunctionSymbols {
		if sym != nil && sym.ID == ident.Symbol.ID {
			fnDecl = declFn
			break
		}
	}
	if fnDecl == nil {
		return
	}

	if len(args) != len(fnDecl.Params) {
		common.AddError(c.diag, c.module.FilePath, callExpr, diagnostics.ErrWrongArgumentCount,
			fmt.Sprintf("wrong number of arguments: got %d, want %d", len(args), len(fnDecl.Params)))
		return
	}

	for i, arg := range args {
		if arg == nil {
			continue
		}
		paramType := typeinfo.TypeFromSyntax(fnDecl.Params[i].Type)
		if paramType == nil {
			continue
		}
		if typeinfo.CheckNumericCompatibility(paramType, arg.Type()) != typeinfo.Compatible {
			common.AddError(c.diag, c.module.FilePath, callExpr.Args[i], diagnostics.ErrTypeMismatch,
				fmt.Sprintf("cannot implicitly convert %s to %s",
					typeinfo.TypeText(arg.Type()), typeinfo.TypeText(paramType)))
		}
	}
}

func Check(module *context.Module, diag *diagnostics.DiagnosticBag) {
	if module == nil || module.Decls == nil || module.Bindings == nil || diag == nil {
		return
	}
	(&checker{module: module, diag: diag}).checkModule()
}
