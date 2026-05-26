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
			Bindings:   make([]hir.Binding, 0),
			Returns:    make([]ir.Expr, 0),
		}
		for _, param := range declFn.Decl.Params {
			name := ""
			if param.Name != nil {
				name = param.Name.Name
			}
			fn.Params = append(fn.Params, ir.Param{Name: name, Type: ir.TypeText(param.Type)})
		}
		for _, stmt := range declFn.Decl.Body.Stmts {
			switch node := stmt.(type) {
			case *ast.LetDecl:
				value, ok := module.Types.LookupExpr(node.Value)
				if !ok {
					continue
				}
				resolution, ok := module.Bindings.LookupNode(node.Name)
				if !ok || resolution == nil || resolution.Symbol == nil {
					continue
				}
				fn.Bindings = append(fn.Bindings, hir.Binding{Name: resolution.Symbol.Name, Value: lowerExpr(value)})
			case *ast.ConstDecl:
				value, ok := module.Types.LookupExpr(node.Value)
				if !ok {
					continue
				}
				resolution, ok := module.Bindings.LookupNode(node.Name)
				if !ok || resolution == nil || resolution.Symbol == nil {
					continue
				}
				fn.Bindings = append(fn.Bindings, hir.Binding{Name: resolution.Symbol.Name, Value: lowerExpr(value)})
			case *ast.ReturnStmt:
				if node.Value == nil {
					continue
				}
				value, ok := module.Types.LookupExpr(node.Value)
				if !ok {
					continue
				}
				fn.Returns = append(fn.Returns, lowerExpr(value))
			}
		}
		out.Funcs = append(out.Funcs, fn)
	}
	return out
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
