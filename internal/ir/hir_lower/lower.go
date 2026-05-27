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

func LowerTyped(module *context.Module) *hir.Module {
	if module == nil || module.Types == nil || module.Decls == nil || module.Bindings == nil {
		return nil
	}
	in := module.Types
	out := &hir.Module{
		Name:    module.ImportPath,
		Externs: make([]hir.Extern, 0, len(in.Externs)),
		Funcs:   make([]*hir.Function, 0, len(module.Decls.Functions)),
	}
	for _, ex := range in.Externs {
		if ex.Symbol == nil || ex.Decl == nil {
			continue
		}
		params := make([]ir.Param, 0, len(ex.Decl.Params))
		for _, param := range ex.Decl.Params {
			name := ""
			if param.Name != nil {
				name = param.Name.Name
			}
			params = append(params, ir.Param{Name: name, Type: ir.TypeText(param.Type)})
		}
		out.Externs = append(out.Externs, hir.Extern{
			Name:       ex.Symbol.Name,
			Params:     params,
			ReturnType: ir.TypeText(ex.Decl.ReturnType),
		})
	}
	for _, declFn := range module.Decls.Functions {
		if declFn == nil || declFn.Decl == nil {
			continue
		}
		fnSym, ok := module.Bindings.FunctionSymbols[declFn.Decl]
		if !ok || fnSym == nil {
			continue
		}
		retType, ok := module.Types.LookupFunctionReturn(declFn.Decl)
		if !ok {
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
			if param.Name != nil {
				if resolution, ok := module.Bindings.LookupNode(param.Name); ok && resolution != nil && resolution.Symbol != nil {
					name = symbolName(resolution.Symbol)
				} else {
					name = param.Name.Name
				}
			}
			fn.Params = append(fn.Params, ir.Param{Name: name, Type: typeinfo.TypeText(typeinfo.TypeFromSyntax(param.Type))})
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
		resolution, ok := module.Bindings.LookupNode(node.Name)
		if !ok || resolution == nil || resolution.Symbol == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "let binding missing symbol", Location: node.Loc()})
			return
		}
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			if value, ok := module.Types.LookupExpr(node.Value); ok {
				valueExpr = lowerExpr(value)
			} else {
				valueExpr = &ir.InvalidExpr{Message: "invalid initializer", Type: "<invalid>"}
			}
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(resolution.Symbol), Constant: false, Value: valueExpr, Location: node.Loc()})
	case *ast.ConstDecl:
		resolution, ok := module.Bindings.LookupNode(node.Name)
		if !ok || resolution == nil || resolution.Symbol == nil {
			out.Stmts = append(out.Stmts, &hir.Invalid{Message: "const binding missing symbol", Location: node.Loc()})
			return
		}
		valueExpr := ir.Expr(&ir.InvalidExpr{Message: "missing initializer", Type: "<invalid>"})
		if node.Value != nil {
			if value, ok := module.Types.LookupExpr(node.Value); ok {
				valueExpr = lowerExpr(value)
			} else {
				valueExpr = &ir.InvalidExpr{Message: "invalid initializer", Type: "<invalid>"}
			}
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: symbolName(resolution.Symbol), Constant: true, Value: valueExpr, Location: node.Loc()})
	case *ast.IfStmt:
		condExpr := ir.Expr(&ir.InvalidExpr{Message: "invalid condition", Type: "<invalid>"})
		if cond, ok := module.Types.LookupExpr(node.Cond); ok {
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
		if value, ok := module.Types.LookupExpr(node.Value); ok {
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
		if cond, ok := module.Types.LookupExpr(node.Cond); ok {
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
