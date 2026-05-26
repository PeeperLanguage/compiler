package typechecher

import (
	"fmt"
	"strconv"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/common"
	"compiler/internal/analysis/semantics/declinfo"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
)

func Check(module *context.Module, diag *diagnostics.DiagnosticBag) bool {
	if module == nil || module.Decls == nil || module.Bindings == nil {
		return false
	}
	module.Types = &typeinfo.ModuleInfo{
		Externs:   append([]declinfo.ExternDecl(nil), module.Decls.Externs...),
		Functions: make([]*typeinfo.Function, 0, len(module.Decls.Functions)),
	}
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
		fn := &typeinfo.Function{
			Symbol:   sym,
			Decl:     declFn.Decl,
			Scope:    declFn.Scope,
			Bindings: make([]typeinfo.Binding, 0),
			Returns:  make([]typeinfo.Return, 0),
		}
		for _, stmt := range declFn.Decl.Body.Stmts {
			switch node := stmt.(type) {
			case *ast.LetDecl:
				typedExpr, ok := typeExpr(module, node.Value, diag)
				if !ok {
					return false
				}
				bindRes, ok := module.Bindings.LookupNode(node.Name)
				if !ok || bindRes.Symbol == nil {
					common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "missing binding symbol")
					return false
				}
				fn.Bindings = append(fn.Bindings, typeinfo.Binding{Symbol: bindRes.Symbol, Value: typedExpr})
			case *ast.ConstDecl:
				typedExpr, ok := typeExpr(module, node.Value, diag)
				if !ok {
					return false
				}
				bindRes, ok := module.Bindings.LookupNode(node.Name)
				if !ok || bindRes.Symbol == nil {
					common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "missing binding symbol")
					return false
				}
				fn.Bindings = append(fn.Bindings, typeinfo.Binding{Symbol: bindRes.Symbol, Value: typedExpr})
			case *ast.ReturnStmt:
				if node.Value == nil {
					common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidReturn, "return value required")
					return false
				}
				typedExpr, ok := typeExpr(module, node.Value, diag)
				if !ok {
					return false
				}
				fn.Returns = append(fn.Returns, typeinfo.Return{Stmt: node, Value: typedExpr})
			default:
				common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidStatement, "unsupported statement for arithmetic flow")
				return false
			}
		}
		if len(fn.Returns) == 0 {
			common.AddError(diag, module.FilePath, declFn.Decl, diagnostics.ErrMissingReturn, "function must contain return")
			return false
		}
		module.Types.Functions = append(module.Types.Functions, fn)
	}
	return true
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
		return &typeinfo.IntLit{Value: int32(v)}, true
	case *ast.Ident:
		resolution, ok := module.Bindings.LookupNode(node)
		if !ok || resolution == nil || resolution.Symbol == nil {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrUndefinedSymbol, "unknown identifier `"+node.Name+"`")
			return nil, false
		}
		return &typeinfo.Ident{Symbol: resolution.Symbol}, true
	case *ast.UnaryExpr:
		if node.Op != "-" && node.Op != "!" {
			common.AddError(diag, module.FilePath, node, diagnostics.ErrInvalidOperation, "unsupported unary operator `"+node.Op+"`")
			return nil, false
		}
		arg, ok := typeExpr(module, node.Expr, diag)
		if !ok {
			return nil, false
		}
		return &typeinfo.Unary{Op: node.Op, Arg: arg}, true
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
		return &typeinfo.Binary{Op: node.Op, Left: left, Right: right}, true
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
