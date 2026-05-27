package hir_lower

import (
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
			retType = ir.TypeText(declFn.Decl.ReturnType)
		}
		fn := &hir.Function{
			Name:       fnSym.Name,
			Params:     make([]ir.Param, 0, len(declFn.Decl.Params)),
			ReturnType: retType,
			Body:       &hir.Block{Stmts: make([]hir.Stmt, 0)},
		}
		for _, param := range declFn.Decl.Params {
			name := ""
			if param.Name != nil {
				name = param.Name.Name
			}
			fn.Params = append(fn.Params, ir.Param{Name: name, Type: ir.TypeText(param.Type)})
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
	for _, stmt := range block.Stmts {
		appendStmt(module, out, stmt)
	}
}

func appendStmt(module *context.Module, out *hir.Block, stmt ast.Stmt) {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0)}
		appendBlock(module, block, node)
		out.Stmts = append(out.Stmts, block)
	case *ast.LetDecl:
		value, ok := module.Types.LookupExpr(node.Value)
		if !ok {
			return
		}
		resolution, ok := module.Bindings.LookupNode(node.Name)
		if !ok || resolution == nil || resolution.Symbol == nil {
			return
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: resolution.Symbol.Name, Value: lowerExpr(value)})
	case *ast.ConstDecl:
		value, ok := module.Types.LookupExpr(node.Value)
		if !ok {
			return
		}
		resolution, ok := module.Bindings.LookupNode(node.Name)
		if !ok || resolution == nil || resolution.Symbol == nil {
			return
		}
		out.Stmts = append(out.Stmts, &hir.Binding{Name: resolution.Symbol.Name, Value: lowerExpr(value)})
	case *ast.IfStmt:
		cond, ok := module.Types.LookupExpr(node.Cond)
		if !ok {
			return
		}
		ifStmt := &hir.If{
			Cond: lowerExpr(cond),
			Then: &hir.Block{Stmts: make([]hir.Stmt, 0)},
		}
		appendBlock(module, ifStmt.Then, node.Then)
		if node.Else != nil {
			ifStmt.Else = lowerElse(module, node.Else)
		}
		out.Stmts = append(out.Stmts, ifStmt)
	case *ast.ReturnStmt:
		if node.Value == nil {
			return
		}
		value, ok := module.Types.LookupExpr(node.Value)
		if !ok {
			return
		}
		out.Stmts = append(out.Stmts, &hir.Return{Value: lowerExpr(value)})
	}
}

func lowerElse(module *context.Module, stmt ast.Stmt) hir.Stmt {
	switch node := stmt.(type) {
	case *ast.BlockStmt:
		block := &hir.Block{Stmts: make([]hir.Stmt, 0)}
		appendBlock(module, block, node)
		return block
	case *ast.IfStmt:
		cond, ok := module.Types.LookupExpr(node.Cond)
		if !ok {
			return nil
		}
		out := &hir.If{
			Cond: lowerExpr(cond),
			Then: &hir.Block{Stmts: make([]hir.Stmt, 0)},
		}
		appendBlock(module, out.Then, node.Then)
		if node.Else != nil {
			out.Else = lowerElse(module, node.Else)
		}
		return out
	default:
		return nil
	}
}

func lowerExpr(expr typeinfo.Expr) ir.Expr {
	switch e := expr.(type) {
	case *typeinfo.IntLit:
		return &ir.IntLit{Value: e.Value}
	case *typeinfo.Ident:
		if e.Symbol == nil {
			return &ir.IntLit{Value: 0}
		}
		return &ir.Ident{Name: e.Symbol.Name}
	case *typeinfo.Unary:
		return &ir.Unary{Op: e.Op, Arg: lowerExpr(e.Arg)}
	case *typeinfo.Binary:
		return &ir.Binary{Op: e.Op, Left: lowerExpr(e.Left), Right: lowerExpr(e.Right)}
	default:
		return &ir.IntLit{Value: 0}
	}
}
