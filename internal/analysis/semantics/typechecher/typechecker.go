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

func Check(module *context.Module, diag *diagnostics.DiagnosticBag) bool {
	if module == nil || module.Decls == nil || module.Bindings == nil {
		return false
	}
	module.Types = typeinfo.NewModuleInfo()
	module.Types.Externs = append(module.Types.Externs, module.Decls.Externs...)
	if len(module.Decls.Functions) == 0 {
		return true
	}
	for _, declFn := range module.Decls.Functions {
		if declFn == nil || declFn.Decl == nil || declFn.Scope == nil {
			continue
		}
		if !checkFunctionShape(module, declFn.Decl, diag) {
			return false
		}
		sym, ok := module.Bindings.FunctionSymbols[declFn.Decl]
		if !ok || sym == nil {
			common.AddError(diag, module.FilePath, declFn.Decl, diagnostics.ErrUndefinedSymbol, "missing function binding")
			return false
		}
		module.Types.BindSymbolType(sym, typeinfo.TypeFromSyntax(declFn.Decl.ReturnType))
		for _, param := range declFn.Decl.Params {
			if param.Name == nil {
				continue
			}
			res, ok := module.Bindings.LookupNode(param.Name)
			if !ok || res == nil || res.Symbol == nil {
				common.AddError(diag, module.FilePath, declFn.Decl, diagnostics.ErrUndefinedSymbol, "missing parameter binding")
				return false
			}
			module.Types.BindSymbolType(res.Symbol, typeinfo.TypeFromSyntax(param.Type))
		}
		sawReturn, ok := checkBlock(module, declFn.Decl, declFn.Decl.Body, typeinfo.TypeFromSyntax(declFn.Decl.ReturnType), diag)
		if !ok {
			return false
		}
		if !sawReturn {
			common.AddError(diag, module.FilePath, declFn.Decl, diagnostics.ErrMissingReturn, "function must contain return")
			return false
		}
	}
	return true
}

func checkBlock(module *context.Module, fn *ast.FnDecl, block *ast.BlockStmt, returnType typeinfo.Type, diag *diagnostics.DiagnosticBag) (bool, bool) {
	if block == nil {
		return false, true
	}
	sawReturn := false
	for _, stmt := range block.Stmts {
		stmtReturn, ok := checkStmt(module, fn, stmt, returnType, diag)
		if !ok {
			return false, false
		}
		sawReturn = sawReturn || stmtReturn
	}
	return sawReturn, true
}

func checkStmt(module *context.Module, fn *ast.FnDecl, stmt ast.Stmt, returnType typeinfo.Type, diag *diagnostics.DiagnosticBag) (bool, bool) {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		return checkBlock(module, fn, node, returnType, diag)
	case *ast.LetDecl:
		declType := typeinfo.TypeFromSyntax(node.Type)
		typedExpr, ok := typeExpr(module, node.Value, declType, diag)
		if !ok {
			return false, false
		}
		bindRes, ok := module.Bindings.LookupNode(node.Name)
		if !ok || bindRes.Symbol == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "missing binding symbol")
			return false, false
		}
		module.Types.BindExpr(node.Value, typedExpr)
		module.Types.BindSymbolType(bindRes.Symbol, typedExpr.Type())
		return false, true
	case *ast.ConstDecl:
		declType := typeinfo.TypeFromSyntax(node.Type)
		typedExpr, ok := typeExpr(module, node.Value, declType, diag)
		if !ok {
			return false, false
		}
		bindRes, ok := module.Bindings.LookupNode(node.Name)
		if !ok || bindRes.Symbol == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "missing binding symbol")
			return false, false
		}
		module.Types.BindExpr(node.Value, typedExpr)
		module.Types.BindSymbolType(bindRes.Symbol, typedExpr.Type())
		return false, true
	case *ast.ReturnStmt:
		if node.Value == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return false, false
		}
		typedExpr, ok := typeExpr(module, node.Value, returnType, diag)
		if !ok {
			return false, false
		}
		module.Types.BindExpr(node.Value, typedExpr)
		module.Types.BindFunctionReturn(fn, typedExpr.Type())
		return true, true
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
			return false, false
		}
		cond, ok := typeExpr(module, node.Cond, nil, diag)
		if !ok {
			return false, false
		}
		if !isConditionType(cond.Type()) {
			common.AddError(diag, module.FilePath, node.Cond, diagnostics.ErrInvalidOperation, "if condition must be bool or scalar number")
			return false, false
		}
		module.Types.BindExpr(node.Cond, cond)
		thenReturn, ok := checkBlock(module, fn, node.Then, returnType, diag)
		if !ok {
			return false, false
		}
		if node.Else == nil {
			return false, true
		}
		elseReturn, ok := checkStmt(module, fn, node.Else, returnType, diag)
		if !ok {
			return false, false
		}
		return thenReturn && elseReturn, true
	case *ast.ExprStmt:
		common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidStatement, "expression statements unsupported in current compiler stage")
		return false, false
	default:
		common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidStatement, "unsupported statement for arithmetic flow")
		return false, false
	}
}

func checkFunctionShape(module *context.Module, decl *ast.FnDecl, diag *diagnostics.DiagnosticBag) bool {
	if decl == nil {
		return false
	}
	if decl.Receiver != nil {
		common.AddError(diag, module.FilePath, decl, diagnostics.ErrInvalidMethodReceiver, "receivers not supported in current compiler stage")
		return false
	}
	retType := typeinfo.TypeFromSyntax(decl.ReturnType)
	if !isSupportedScalarType(retType) {
		common.AddError(diag, module.FilePath, decl, diagnostics.ErrInvalidReturn, "function return type must be builtin integer or f32/f64 in current compiler stage")
		return false
	}
	for _, param := range decl.Params {
		if !isSupportedScalarType(typeinfo.TypeFromSyntax(param.Type)) {
			common.AddError(diag, module.FilePath, param.Name, diagnostics.ErrInvalidType, "parameter type must be builtin integer or f32/f64 in current compiler stage")
			return false
		}
	}
	return true
}

func typeExpr(module *context.Module, expr ast.Expr, expected typeinfo.Type, diag *diagnostics.DiagnosticBag) (typeinfo.Expr, bool) {
	switch node := expr.(type) {
	case *ast.NumberLit:
		typedExpr, ok := typeNumber(module, node, expected, diag)
		if !ok {
			return nil, false
		}
		module.Types.BindExpr(node, typedExpr)
		return typedExpr, true
	case *ast.Ident:
		resolution, ok := module.Bindings.LookupNode(node)
		if !ok || resolution == nil || resolution.Symbol == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "unknown identifier `"+node.Name+"`")
			return nil, false
		}
		exprType, ok := module.Types.LookupSymbolType(resolution.Symbol)
		if !ok || exprType == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "missing identifier type `"+node.Name+"`")
			return nil, false
		}
		expr := &typeinfo.Ident{Symbol: resolution.Symbol, ExprType: exprType}
		module.Types.BindExpr(node, expr)
		return expr, true
	case *ast.UnaryExpr:
		if node.Op != "-" && node.Op != "!" {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported unary operator `"+node.Op+"`")
			return nil, false
		}
		arg, ok := typeExpr(module, node.Expr, expected, diag)
		if !ok {
			return nil, false
		}
		if !isSupportedScalarType(arg.Type()) && !typeinfo.SameType(arg.Type(), &typeinfo.BoolType{}) {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported unary operand type")
			return nil, false
		}
		exprType := arg.Type()
		if node.Op == "!" {
			exprType = &typeinfo.BoolType{}
		}
		expr := &typeinfo.Unary{Op: node.Op, Arg: arg, ExprType: exprType}
		module.Types.BindExpr(node, expr)
		return expr, true
	case *ast.BinaryExpr:
		if !allowedOp(node.Op) {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported binary operator `"+node.Op+"`")
			return nil, false
		}
		left, ok := typeExpr(module, node.Left, expected, diag)
		if !ok {
			return nil, false
		}
		right, ok := typeExpr(module, node.Right, expected, diag)
		if !ok {
			return nil, false
		}
		left, right, ok = coerceBinaryLiteralSides(module, node, left, right, diag)
		if !ok {
			return nil, false
		}
		if !typeinfo.SameType(left.Type(), right.Type()) {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrTypeMismatch, fmt.Sprintf("operand types mismatch: %s vs %s", typeinfo.TypeText(left.Type()), typeinfo.TypeText(right.Type())))
			return nil, false
		}
		exprType := left.Type()
		switch node.Op {
		case "==", "!=", "<", "<=", ">", ">=", "&&", "||":
			exprType = &typeinfo.BoolType{}
		}
		if !validBinaryTypes(node.Op, left.Type()) {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported operand type for operator `"+node.Op+"`")
			return nil, false
		}
		expr := &typeinfo.Binary{Op: node.Op, Left: left, Right: right, ExprType: exprType}
		module.Types.BindExpr(node, expr)
		return expr, true
	default:
		common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidExpression, "unsupported expression for arithmetic flow")
		return nil, false
	}
}

func typeNumber(module *context.Module, node *ast.NumberLit, expected typeinfo.Type, diag *diagnostics.DiagnosticBag) (typeinfo.Expr, bool) {
	if node == nil {
		return nil, false
	}
	if expected != nil {
		switch typ := expected.(type) {
		case *typeinfo.IntegerType:
			if !numeric.FitsIntegerLiteral(node.Value, typ.Bits, typ.Signed) {
				common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("literal `%s` does not fit %s", node.Value, typ.Text()))
				return nil, false
			}
			return &typeinfo.IntLit{Value: node.Value, ExprType: typ}, true
		case *typeinfo.FloatType:
			if numeric.IsFloat(node.Value) {
				if !numeric.FitsFloatLiteral(node.Value, typ.Bits) {
					common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("literal `%s` does not fit %s", node.Value, typ.Text()))
					return nil, false
				}
			} else if !numeric.FitsIntegerLiteralInFloat(node.Value, typ.Bits) {
				common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("literal `%s` does not fit %s", node.Value, typ.Text()))
				return nil, false
			}
			return &typeinfo.FloatLit{Value: node.Value, ExprType: typ}, true
		}
	}
	if numeric.IsFloat(node.Value) {
		if !numeric.FitsFloatLiteral(node.Value, 64) {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("invalid f64 literal `%s`", node.Value))
			return nil, false
		}
		return &typeinfo.FloatLit{Value: node.Value, ExprType: &typeinfo.FloatType{Bits: 64}}, true
	}
	if !numeric.FitsIntegerLiteral(node.Value, 32, true) {
		common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("integer literal `%s` requires explicit type", node.Value))
		return nil, false
	}
	return &typeinfo.IntLit{Value: node.Value, ExprType: &typeinfo.IntegerType{Signed: true, Bits: 32}}, true
}

func coerceBinaryLiteralSides(module *context.Module, node *ast.BinaryExpr, left, right typeinfo.Expr, diag *diagnostics.DiagnosticBag) (typeinfo.Expr, typeinfo.Expr, bool) {
	if node == nil {
		return left, right, true
	}
	if _, ok := node.Left.(*ast.NumberLit); ok && isSupportedScalarType(right.Type()) {
		if typed, ok := typeNumber(module, node.Left.(*ast.NumberLit), right.Type(), diag); ok {
			left = typed
		} else {
			return nil, nil, false
		}
	}
	if _, ok := node.Right.(*ast.NumberLit); ok && isSupportedScalarType(left.Type()) {
		if typed, ok := typeNumber(module, node.Right.(*ast.NumberLit), left.Type(), diag); ok {
			right = typed
		} else {
			return nil, nil, false
		}
	}
	return left, right, true
}

func isSupportedScalarType(typ typeinfo.Type) bool {
	switch typ.(type) {
	case *typeinfo.IntegerType, *typeinfo.FloatType:
		return true
	default:
		return false
	}
}

func isConditionType(typ typeinfo.Type) bool {
	if _, ok := typ.(*typeinfo.BoolType); ok {
		return true
	}
	return isSupportedScalarType(typ)
}

func validBinaryTypes(op string, typ typeinfo.Type) bool {
	switch op {
	case "+", "-", "*", "/":
		return isSupportedScalarType(typ)
	case "%":
		_, ok := typ.(*typeinfo.IntegerType)
		return ok
	case "==", "!=", "<", "<=", ">", ">=":
		return isSupportedScalarType(typ)
	case "&&", "||":
		return isConditionType(typ)
	default:
		return false
	}
}

func allowedOp(op string) bool {
	switch op {
	case "+", "-", "*", "/", "%", "==", "!=", "<", "<=", ">", ">=", "&&", "||":
		return true
	default:
		return false
	}
}
