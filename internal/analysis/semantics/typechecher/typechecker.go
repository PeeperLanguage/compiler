package typechecher

import (
	"fmt"
	"strconv"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
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
		module.Types.BindSymbolType(sym, "i32")
		sawReturn, ok := checkBlock(module, declFn.Decl, declFn.Decl.Body, diag)
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

func checkBlock(module *context.Module, fn *ast.FnDecl, block *ast.BlockStmt, diag *diagnostics.DiagnosticBag) (bool, bool) {
	if block == nil {
		return false, true
	}
	sawReturn := false
	for _, stmt := range block.Stmts {
		stmtReturn, ok := checkStmt(module, fn, stmt, diag)
		if !ok {
			return false, false
		}
		sawReturn = sawReturn || stmtReturn
	}
	return sawReturn, true
}

func checkStmt(module *context.Module, fn *ast.FnDecl, stmt ast.Stmt, diag *diagnostics.DiagnosticBag) (bool, bool) {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		return checkBlock(module, fn, node, diag)
	case *ast.LetDecl:
		typedExpr, ok := typeExpr(module, node.Value, diag)
		if !ok {
			return false, false
		}
		bindRes, ok := module.Bindings.LookupNode(node.Name)
		if !ok || bindRes.Symbol == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "missing binding symbol")
			return false, false
		}
		module.Types.BindExpr(node.Value, typedExpr)
		module.Types.BindSymbolType(bindRes.Symbol, typeinfo.ExprTypeName(typedExpr))
		return false, true
	case *ast.ConstDecl:
		typedExpr, ok := typeExpr(module, node.Value, diag)
		if !ok {
			return false, false
		}
		bindRes, ok := module.Bindings.LookupNode(node.Name)
		if !ok || bindRes.Symbol == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "missing binding symbol")
			return false, false
		}
		module.Types.BindExpr(node.Value, typedExpr)
		module.Types.BindSymbolType(bindRes.Symbol, typeinfo.ExprTypeName(typedExpr))
		return false, true
	case *ast.ReturnStmt:
		if node.Value == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
			return false, false
		}
		typedExpr, ok := typeExpr(module, node.Value, diag)
		if !ok {
			return false, false
		}
		module.Types.BindExpr(node.Value, typedExpr)
		module.Types.BindFunctionReturn(fn, typeinfo.ExprTypeName(typedExpr))
		return true, true
	case *ast.IfStmt:
		if node.Cond == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidStatement, "if condition required")
			return false, false
		}
		cond, ok := typeExpr(module, node.Cond, diag)
		if !ok {
			return false, false
		}
		module.Types.BindExpr(node.Cond, cond)
		thenReturn, ok := checkBlock(module, fn, node.Then, diag)
		if !ok {
			return false, false
		}
		if node.Else == nil {
			return false, true
		}
		elseReturn, ok := checkStmt(module, fn, node.Else, diag)
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
	if !common.IsI32Type(decl.ReturnType) {
		common.AddError(diag, module.FilePath, decl, diagnostics.ErrInvalidReturn, "function return type must be i32 in current compiler stage")
		return false
	}
	for _, param := range decl.Params {
		if !common.IsI32Type(param.Type) {
			common.AddError(diag, module.FilePath, param.Name, diagnostics.ErrInvalidType, "parameter type must be i32 in current compiler stage")
			return false
		}
	}
	return true
}

func typeExpr(module *context.Module, expr ast.Expr, diag *diagnostics.DiagnosticBag) (typeinfo.Expr, bool) {
	switch node := expr.(type) {
	case *ast.NumberLit:
		v, err := strconv.ParseInt(node.Value, 0, 32)
		if err != nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidNumber, fmt.Sprintf("invalid i32 literal `%s`", node.Value))
			return nil, false
		}
		expr := &typeinfo.IntLit{Value: int32(v)}
		module.Types.BindExpr(node, expr)
		return expr, true
	case *ast.Ident:
		resolution, ok := module.Bindings.LookupNode(node)
		if !ok || resolution == nil || resolution.Symbol == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "unknown identifier `"+node.Name+"`")
			return nil, false
		}
		expr := &typeinfo.Ident{Symbol: resolution.Symbol}
		module.Types.BindExpr(node, expr)
		return expr, true
	case *ast.UnaryExpr:
		if node.Op != "-" && node.Op != "!" {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported unary operator `"+node.Op+"`")
			return nil, false
		}
		arg, ok := typeExpr(module, node.Expr, diag)
		if !ok {
			return nil, false
		}
		expr := &typeinfo.Unary{Op: node.Op, Arg: arg}
		module.Types.BindExpr(node, expr)
		return expr, true
	case *ast.BinaryExpr:
		if !allowedOp(node.Op) {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported binary operator `"+node.Op+"`")
			return nil, false
		}
		left, ok := typeExpr(module, node.Left, diag)
		if !ok {
			return nil, false
		}
		right, ok := typeExpr(module, node.Right, diag)
		if !ok {
			return nil, false
		}
		expr := &typeinfo.Binary{Op: node.Op, Left: left, Right: right}
		module.Types.BindExpr(node, expr)
		return expr, true
	default:
		common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidExpression, "unsupported expression for arithmetic flow")
		return nil, false
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
