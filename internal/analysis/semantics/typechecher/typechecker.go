package typechecher

import (
	"fmt"

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

func (c *checker) checkModule() bool {
	if c == nil || c.module == nil || c.module.Decls == nil || c.module.Bindings == nil {
		return false
	}
	c.module.Types = typeinfo.NewModuleInfo()
	c.module.Types.Externs = append(c.module.Types.Externs, c.module.Decls.Externs...)
	for _, declFn := range c.module.Decls.Functions {
		if declFn == nil || declFn.Decl == nil || declFn.Scope == nil {
			continue
		}
		if !c.checkFunction(declFn.Decl) {
			return false
		}
	}
	return true
}

func (c *checker) checkFunction(decl *ast.FnDecl) bool {
	if c == nil || decl == nil {
		return false
	}
	if !c.checkFunctionShape(decl) {
		return false
	}
	sym, ok := c.module.Bindings.FunctionSymbols[decl]
	if !ok || sym == nil {
		common.AddError(c.diag, c.module.FilePath, decl, diagnostics.ErrUndefinedSymbol, "missing function binding")
		return false
	}
	c.module.Types.BindSymbolType(sym, typeinfo.TypeFromSyntax(decl.ReturnType))
	for _, param := range decl.Params {
		if param.Name == nil {
			continue
		}
		res, ok := c.module.Bindings.LookupNode(param.Name)
		if !ok || res == nil || res.Symbol == nil {
			common.AddError(c.diag, c.module.FilePath, decl, diagnostics.ErrUndefinedSymbol, "missing parameter binding")
			return false
		}
		c.module.Types.BindSymbolType(res.Symbol, typeinfo.TypeFromSyntax(param.Type))
	}
	returnType := typeinfo.TypeFromSyntax(decl.ReturnType)
	if !c.checkBlock(decl, decl.Body, returnType) {
		return false
	}
	return true
}

func (c *checker) checkBlock(fn *ast.FnDecl, block *ast.BlockStmt, returnType typeinfo.Type) bool {
	if block == nil {
		return true
	}
	valid := true
	for _, stmt := range block.Stmts {
		if !c.checkStmt(fn, stmt, returnType) {
			valid = false
		}
	}
	return valid
}

func (c *checker) checkStmt(fn *ast.FnDecl, stmt ast.Stmt, returnType typeinfo.Type) bool {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		return c.checkBlock(fn, node, returnType)
	case *ast.LetDecl:
		return c.checkBinding(node, false)
	case *ast.ConstDecl:
		return c.checkBinding(node, true)
	case *ast.ReturnStmt:
		if node.Value == nil {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return false
		}
		typedExpr, ok := c.typeExpr(node.Value, returnType)
		if !ok {
			return false
		}
		if !typeinfo.Assignable(returnType, typedExpr.Type()) {
			common.AddError(c.diag, c.module.FilePath, node.Value, diagnostics.ErrTypeMismatch, fmt.Sprintf("cannot return %s from function returning %s", typeinfo.TypeText(typedExpr.Type()), typeinfo.TypeText(returnType)))
			return false
		}
		c.module.Types.BindExpr(node.Value, typedExpr)
		c.module.Types.BindFunctionReturn(fn, typedExpr.Type())
		return true
	case *ast.IfStmt:
		valid := true
		if node.Cond == nil {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
			valid = false
		} else {
			cond, ok := c.typeExpr(node.Cond, nil)
			if !ok {
				valid = false
			} else {
				c.module.Types.BindExpr(node.Cond, cond)
				if !typeinfo.IsInvalid(cond.Type()) && !typeinfo.IsUnknown(cond.Type()) && !c.isConditionType(cond.Type()) {
					common.AddError(c.diag, c.module.FilePath, node.Cond, diagnostics.ErrInvalidOperation, "if condition must be bool or scalar number")
					valid = false
				}
			}
		}
		if !c.checkBlock(fn, node.Then, returnType) {
			valid = false
		}
		if node.Else != nil && !c.checkStmt(fn, node.Else, returnType) {
			valid = false
		}
		return valid
	case *ast.ExprStmt:
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidStatement, "expression statements unsupported in current compiler stage")
		return false
	default:
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidStatement, "unsupported statement for arithmetic flow")
		return false
	}
}

func (c *checker) checkBinding(node ast.Stmt, requireInitializer bool) bool {
	if c == nil || node == nil {
		return false
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
		return false
	}

	bindRes, ok := c.module.Bindings.LookupNode(nameNode)
	if !ok || bindRes == nil || bindRes.Symbol == nil {
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrUndefinedSymbol, "missing binding symbol")
		return false
	}

	if value == nil {
		if requireInitializer {
			c.module.Types.BindSymbolType(bindRes.Symbol, &typeinfo.InvalidType{})
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrMissingInitializer, "missing initializer for const declaration")
			return false
		}
		if declType == nil {
			c.module.Types.BindSymbolType(bindRes.Symbol, &typeinfo.InvalidType{})
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrMissingType, "let declaration needs type or initializer")
			return false
		}
		c.module.Types.BindSymbolType(bindRes.Symbol, declType)
		return true
	}

	typedExpr, ok := c.typeExpr(value, declType)
	if ok {
		if declType != nil && !typeinfo.Assignable(declType, typedExpr.Type()) {
			common.AddError(c.diag, c.module.FilePath, value, diagnostics.ErrTypeMismatch, fmt.Sprintf("cannot assign %s to %s", typeinfo.TypeText(typedExpr.Type()), typeinfo.TypeText(declType)))
			c.module.Types.BindSymbolType(bindRes.Symbol, &typeinfo.InvalidType{})
			return false
		}
		c.module.Types.BindExpr(value, typedExpr)
		if declType != nil {
			c.module.Types.BindSymbolType(bindRes.Symbol, declType)
		} else {
			c.module.Types.BindSymbolType(bindRes.Symbol, typedExpr.Type())
		}
	}
	return ok
}

func (c *checker) checkFunctionShape(decl *ast.FnDecl) bool {
	if decl == nil {
		return false
	}
	if decl.Receiver != nil {
		common.AddError(c.diag, c.module.FilePath, decl, diagnostics.ErrInvalidMethodReceiver, "receivers not supported in current compiler stage")
		return false
	}
	retType := typeinfo.TypeFromSyntax(decl.ReturnType)
	if !c.isSupportedScalarType(retType) {
		common.AddError(c.diag, c.module.FilePath, decl, diagnostics.ErrInvalidReturn, "function return type must be builtin integer or f32/f64 in current compiler stage")
		return false
	}
	for _, param := range decl.Params {
		if !c.isSupportedScalarType(typeinfo.TypeFromSyntax(param.Type)) {
			common.AddError(c.diag, c.module.FilePath, param.Name, diagnostics.ErrInvalidType, "parameter type must be builtin integer or f32/f64 in current compiler stage")
			return false
		}
	}
	return true
}

func (c *checker) typeExpr(expr ast.Expr, expected typeinfo.Type) (typeinfo.Expr, bool) {
	switch node := expr.(type) {
	case *ast.NumberLit:
		typedExpr, ok := c.typeNumber(node, expected)
		if !ok {
			return nil, false
		}
		c.module.Types.BindExpr(node, typedExpr)
		return typedExpr, true
	case *ast.Ident:
		resolution, ok := c.module.Bindings.LookupNode(node)
		if !ok || resolution == nil || resolution.Symbol == nil {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrUndefinedSymbol, "unknown identifier `"+node.Name+"`")
			return nil, false
		}
		exprType, ok := c.module.Types.LookupSymbolType(resolution.Symbol)
		if !ok || exprType == nil {
			exprType = &typeinfo.UnknownType{}
		}
		expr := &typeinfo.Ident{Symbol: resolution.Symbol, ExprType: exprType}
		c.module.Types.BindExpr(node, expr)
		return expr, true
	case *ast.UnaryExpr:
		if node.Op != "-" && node.Op != "!" {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported unary operator `"+node.Op+"`")
			return nil, false
		}
		arg, ok := c.typeExpr(node.Expr, expected)
		if !ok {
			return nil, false
		}
		if typeinfo.IsInvalid(arg.Type()) || typeinfo.IsUnknown(arg.Type()) {
			expr := &typeinfo.Unary{Op: node.Op, Arg: arg, ExprType: &typeinfo.InvalidType{}}
			c.module.Types.BindExpr(node, expr)
			return expr, true
		}
		if !c.isSupportedScalarType(arg.Type()) && !typeinfo.SameType(arg.Type(), &typeinfo.BoolType{}) {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported unary operand type")
			return nil, false
		}
		exprType := arg.Type()
		if node.Op == "!" {
			exprType = &typeinfo.BoolType{}
		}
		expr := &typeinfo.Unary{Op: node.Op, Arg: arg, ExprType: exprType}
		c.module.Types.BindExpr(node, expr)
		return expr, true
	case *ast.BinaryExpr:
		if !c.allowedOp(node.Op) {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported binary operator `"+node.Op+"`")
			return nil, false
		}
		left, ok := c.typeExpr(node.Left, expected)
		if !ok {
			return nil, false
		}
		right, ok := c.typeExpr(node.Right, expected)
		if !ok {
			return nil, false
		}
		left, right, ok = c.coerceBinaryLiteralSides(node, left, right)
		if !ok {
			return nil, false
		}
		if typeinfo.IsInvalid(left.Type()) || typeinfo.IsUnknown(left.Type()) || typeinfo.IsInvalid(right.Type()) || typeinfo.IsUnknown(right.Type()) {
			expr := &typeinfo.Binary{Op: node.Op, Left: left, Right: right, ExprType: &typeinfo.InvalidType{}}
			c.module.Types.BindExpr(node, expr)
			return expr, true
		}
		commonType := typeinfo.CommonNumericType(left.Type(), right.Type())
		if commonType == nil && !typeinfo.Assignable(left.Type(), right.Type()) && !typeinfo.Assignable(right.Type(), left.Type()) {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrTypeMismatch, fmt.Sprintf("operand types mismatch: %s vs %s", typeinfo.TypeText(left.Type()), typeinfo.TypeText(right.Type())))
			return nil, false
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
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported operand type for operator `"+node.Op+"`")
			return nil, false
		}
		expr := &typeinfo.Binary{Op: node.Op, Left: left, Right: right, ExprType: exprType}
		c.module.Types.BindExpr(node, expr)
		return expr, true
	case *ast.CallExpr:
		// Type check function call
		callee, ok := c.typeExpr(node.Callee, expected)
		if !ok {
			return nil, false
		}
		args := make([]typeinfo.Expr, 0, len(node.Args))
		for _, arg := range node.Args {
			argExpr, ok := c.typeExpr(arg, nil)
			if !ok {
				return nil, false
			}
			args = append(args, argExpr)
		}
		// For now, just pass through the callee's type as the call's type
		// TODO: Look up function signature and validate arguments
		callExpr := &typeinfo.Call{Callee: callee, Args: args, ExprType: callee.Type()}
		c.module.Types.BindExpr(node, callExpr)
		return callExpr, true
	default:
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidExpression, "unsupported expression for arithmetic flow")
		return nil, false
	}
}

func (c *checker) typeNumber(node *ast.NumberLit, expected typeinfo.Type) (typeinfo.Expr, bool) {
	if node == nil {
		return nil, false
	}
	if typeinfo.IsInvalid(expected) || typeinfo.IsUnknown(expected) {
		expected = nil
	}
	if expected != nil {
		switch typ := expected.(type) {
		case *typeinfo.IntegerType:
			if !numeric.FitsIntegerLiteral(node.Value, typ.Bits, typ.Signed) {
				common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("literal `%s` does not fit %s", node.Value, typ.Text()))
				return nil, false
			}
			return &typeinfo.IntLit{Value: node.Value, ExprType: typ}, true
		case *typeinfo.FloatType:
			if !numeric.IsFloat(node.Value) {
				common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrTypeMismatch, fmt.Sprintf("integer literal `%s` cannot be used as %s", node.Value, typ.Text()))
				return nil, false
			}
			if !numeric.FitsFloatLiteral(node.Value, typ.Bits) {
				common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("literal `%s` does not fit %s", node.Value, typ.Text()))
				return nil, false
			}
			return &typeinfo.FloatLit{Value: node.Value, ExprType: typ}, true
		}
	}
	if numeric.IsFloat(node.Value) {
		if !numeric.FitsFloatLiteral(node.Value, 64) {
			common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("invalid f64 literal `%s`", node.Value))
			return nil, false
		}
		return &typeinfo.FloatLit{Value: node.Value, ExprType: &typeinfo.FloatType{Bits: 64}}, true
	}
	if !numeric.FitsIntegerLiteral(node.Value, 32, true) {
		common.AddError(c.diag, c.module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("integer literal `%s` requires explicit type", node.Value))
		return nil, false
	}
	return &typeinfo.IntLit{Value: node.Value, ExprType: &typeinfo.IntegerType{Signed: true, Bits: 32}}, true
}

func (c *checker) coerceBinaryLiteralSides(node *ast.BinaryExpr, left, right typeinfo.Expr) (typeinfo.Expr, typeinfo.Expr, bool) {
	if node == nil {
		return left, right, true
	}
	if _, ok := node.Left.(*ast.NumberLit); ok && c.isSupportedScalarType(right.Type()) {
		if typed, ok := c.typeNumber(node.Left.(*ast.NumberLit), right.Type()); ok {
			left = typed
		} else {
			return nil, nil, false
		}
	}
	if _, ok := node.Right.(*ast.NumberLit); ok && c.isSupportedScalarType(left.Type()) {
		if typed, ok := c.typeNumber(node.Right.(*ast.NumberLit), left.Type()); ok {
			right = typed
		} else {
			return nil, nil, false
		}
	}
	return left, right, true
}

func (c *checker) isSupportedScalarType(typ typeinfo.Type) bool {
	switch typ.(type) {
	case *typeinfo.IntegerType, *typeinfo.FloatType:
		return true
	default:
		return false
	}
}

func (c *checker) isConditionType(typ typeinfo.Type) bool {
	if _, ok := typ.(*typeinfo.BoolType); ok {
		return true
	}
	return c.isSupportedScalarType(typ)
}

func (c *checker) validBinaryTypes(op string, typ typeinfo.Type) bool {
	switch op {
	case "+", "-", "*", "/":
		return c.isSupportedScalarType(typ)
	case "%":
		_, ok := typ.(*typeinfo.IntegerType)
		return ok
	case "==", "!=", "<", "<=", ">", ">=":
		return c.isSupportedScalarType(typ)
	case "&&", "||":
		return c.isConditionType(typ)
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

func Check(module *context.Module, diag *diagnostics.DiagnosticBag) bool {
	if module == nil || module.Decls == nil || module.Bindings == nil || diag == nil {
		return false
	}
	c := &checker{module: module, diag: diag}
	return c.checkModule()
}
