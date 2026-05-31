package hir_lower

import (
	"fmt"

	"compiler/internal/analysis/semantics/symbols"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
)

func GenerateHIR(module *context.Module) *hir.Module {
	if module == nil {
		return nil
	}
	out := &hir.Module{
		Name:    module.ImportPath,
		Externs: make([]hir.Extern, 0, len(module.Externs)),
		Funcs:   make([]*hir.Function, 0, len(module.Functions)),
	}
	for _, ex := range module.Externs {
		if ex.Symbol == nil || ex.Decl == nil {
			continue
		}
		fnType, _ := symbolType(ex.Symbol)
		resolvedFnType, _ := fnType.(*typeinfo.FuncType)
		params := make([]ir.Param, 0, len(ex.Decl.Params))
		for i, param := range ex.Decl.Params {
			name := ""
			if param.Name != nil {
				name = param.Name.Name
			}
			paramType := typeinfo.TypeFromSyntax(param.Type)
			if resolvedFnType != nil && i < len(resolvedFnType.Params) && resolvedFnType.Params[i] != nil {
				paramType = resolvedFnType.Params[i]
			}
			params = append(params, ir.Param{Name: name, Type: typeinfo.TypeText(paramType)})
		}
		returnType := typeinfo.TypeFromSyntax(ex.Decl.ReturnType)
		if resolvedFnType != nil && resolvedFnType.Return != nil {
			returnType = resolvedFnType.Return
		}
		out.Externs = append(out.Externs, hir.Extern{
			Name:       ex.Symbol.Name,
			Params:     params,
			ReturnType: typeinfo.TypeText(returnType),
		})
	}
	for _, declFn := range module.Functions {
		if declFn == nil || declFn.Decl == nil {
			continue
		}
		fnSym := declFn.Symbol
		if fnSym == nil {
			continue
		}
		retType, ok := symbolType(fnSym)
		if ok {
			if fnType, ok := retType.(*typeinfo.FuncType); ok && fnType != nil {
				retType = fnType.Return
			}
		}
		if !ok || retType == nil {
			retType = typeinfo.TypeFromSyntax(declFn.Decl.ReturnType)
		}
		fn := &hir.Function{
			Name:       fnSym.Name,
			Params:     make([]ir.Param, 0, len(declFn.Decl.Params)),
			ReturnType: typeinfo.TypeText(retType),
			Body:       &hir.Block{Stmts: make([]hir.Stmt, 0), Location: declFn.Decl.Body.Loc()},
			Location:   declFn.Decl.Loc(),
		}
		for _, param := range declFn.Decl.Params {
			name := ""
			paramType := typeinfo.TypeFromSyntax(param.Type)
			if param.Name != nil {
				if resolution, ok := module.LookupResolution(param.Name); ok && resolution != nil && resolution.Symbol != nil {
					name = symbolName(resolution.Symbol)
					if resolvedType, ok := symbolType(resolution.Symbol); ok {
						paramType = resolvedType
					}
				} else {
					name = param.Name.Name
				}
			}
			fn.Params = append(fn.Params, ir.Param{Name: name, Type: typeinfo.TypeText(paramType)})
		}
		appendBlock(module, fn.Body, declFn.Decl.Body)
		out.Funcs = append(out.Funcs, fn)
	}
	return out
}

func appendBlock(module *context.Module, out *hir.Block, block *ast.BlockStmt) {
	if module == nil || out == nil || block == nil {
		return
	}
	out.Location = block.Loc()
	for _, stmt := range block.Stmts {
		appendStmt(module, out, stmt)
	}
}

func appendStmt(module *context.Module, out *hir.Block, stmt ast.Stmt) {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Loc()}
		appendBlock(module, block, node)
		out.Stmts = append(out.Stmts, block)
	case *ast.LetDecl:
		resolution, ok := module.LookupResolution(node.Name)
		if !ok || resolution == nil || resolution.Symbol == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "let binding missing symbol", Location: node.Loc()})
			return
		}
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			if value, ok := module.LookupTypedExpr(node.Value); ok {
				valueExpr = lowerExpr(value)
			} else {
				valueExpr = &ir.InvalidExpr{Message: "invalid initializer", Type: "<invalid>"}
			}
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(resolution.Symbol), Constant: false, Value: valueExpr, Location: node.Loc()})
	case *ast.ConstDecl:
		resolution, ok := module.LookupResolution(node.Name)
		if !ok || resolution == nil || resolution.Symbol == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "const binding missing symbol", Location: node.Loc()})
			return
		}
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			if value, ok := module.LookupTypedExpr(node.Value); ok {
				valueExpr = lowerExpr(value)
			} else {
				valueExpr = &ir.InvalidExpr{Message: "invalid initializer", Type: "<invalid>"}
			}
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(resolution.Symbol), Constant: true, Value: valueExpr, Location: node.Loc()})
	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if cond, ok := module.LookupTypedExpr(node.Cond); ok {
			condExpr = lowerExpr(cond)
		}
		ifStmt := &hir.If{
			Cond:     condExpr,
			Then:     &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Then.Loc()},
			Location: node.Loc(),
		}
		appendBlock(module, ifStmt.Then, node.Then)
		if node.Else != nil {
			ifStmt.Else = lowerElse(module, node.Else)
		}
		out.Stmts = append(out.Stmts, ifStmt)
	case *ast.ReturnStmt:
		if node.Value == nil {
			out.Stmts = append(out.Stmts, &hir.Return{Value: &ir.InvalidExpr{Message: "missing return value", Type: "<invalid>"}, Location: node.Loc()})
			return
		}
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid return value", Type: "<invalid>"})
		if value, ok := module.LookupTypedExpr(node.Value); ok {
			valueExpr = lowerExpr(value)
		}
		out.Stmts = append(out.Stmts, &hir.Return{Value: valueExpr, Location: node.Loc()})
	}
}

func lowerElse(module *context.Module, stmt ast.Stmt) hir.Stmt {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Loc()}
		appendBlock(module, block, node)
		return block
	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if cond, ok := module.LookupTypedExpr(node.Cond); ok {
			condExpr = lowerExpr(cond)
		}
		out := &hir.If{
			Cond:     condExpr,
			Then:     &hir.Block{Stmts: make([]hir.Stmt, 0), Location: node.Then.Loc()},
			Location: node.Loc(),
		}
		appendBlock(module, out.Then, node.Then)
		if node.Else != nil {
			out.Else = lowerElse(module, node.Else)
		}
		return out
	default:
		return &hir.Invalid{Message: "unsupported else branch", Location: node.Loc()}
	}
}

func lowerExpr(expr typeinfo.Expr) ir.Expr {
	switch e := expr.(type) {
	case *typeinfo.IntLit:
		return &ir.IntLit{Value: e.Value, Type: typeinfo.TypeText(e.Type())}
	case *typeinfo.FloatLit:
		return &ir.FloatLit{Value: e.Value, Type: typeinfo.TypeText(e.Type())}
	case *typeinfo.Ident:
		if e.Symbol == nil {
			return &ir.IntLit{Value: "0", Type: "i32"}
		}
		return &ir.Ident{Name: symbolName(e.Symbol), Type: typeinfo.TypeText(e.Type())}
	case *typeinfo.Unary:
		return &ir.Unary{Op: e.Op, Arg: lowerExpr(e.Arg), Type: typeinfo.TypeText(e.Type())}
	case *typeinfo.Binary:
		return &ir.Binary{Op: e.Op, Left: lowerExpr(e.Left), Right: lowerExpr(e.Right), Type: typeinfo.TypeText(e.Type())}
	case *typeinfo.Call:
		args := make([]ir.Expr, 0, len(e.Args))
		for _, arg := range e.Args {
			args = append(args, lowerExpr(arg))
		}
		return &ir.Call{Callee: lowerExpr(e.Callee), Args: args, Type: typeinfo.TypeText(e.Type())}
	case *typeinfo.As:
		// Lower cast expression - emit a cast operation
		// The type of the As expression is the target type
		return &ir.Cast{
			Expr: lowerExpr(e.Expr),
			Type: typeinfo.TypeText(e.ExprType),
		}
	default:
		return &ir.IntLit{Value: "0", Type: "i32"}
	}
}

func symbolName(sym *symbols.Symbol) string {
	if sym == nil {
		return ""
	}
	return fmt.Sprintf("%s$%d", sym.Name, sym.ID)
}

func symbolType(sym *symbols.Symbol) (typeinfo.Type, bool) {
	if sym == nil || sym.Type == nil {
		return nil, false
	}
	typ, ok := sym.Type.(typeinfo.Type)
	return typ, ok && typ != nil
}
