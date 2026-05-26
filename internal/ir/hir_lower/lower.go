package hir_lower

import (
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
)

func LowerTyped(module *context.Module) *hir.Module {
	if module == nil || module.Types == nil {
		return nil
	}
	in := module.Types
	out := &hir.Module{
		Name:    module.ImportPath,
		Externs: make([]hir.Extern, 0, len(in.Externs)),
		Funcs:   make([]*hir.Function, 0, len(in.Functions)),
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
	for _, typedFn := range in.Functions {
		if typedFn == nil || typedFn.Decl == nil || typedFn.Symbol == nil {
			continue
		}
		fn := &hir.Function{
			Name:       typedFn.Symbol.Name,
			Params:     make([]ir.Param, 0, len(typedFn.Decl.Params)),
			ReturnType: ir.TypeText(typedFn.Decl.ReturnType),
			Bindings:   make([]hir.Binding, 0, len(typedFn.Bindings)),
			Returns:    make([]ir.Expr, 0, len(typedFn.Returns)),
		}
		for _, param := range typedFn.Decl.Params {
			name := ""
			if param.Name != nil {
				name = param.Name.Name
			}
			fn.Params = append(fn.Params, ir.Param{Name: name, Type: ir.TypeText(param.Type)})
		}
		for _, bind := range typedFn.Bindings {
			if bind.Symbol == nil {
				continue
			}
			fn.Bindings = append(fn.Bindings, hir.Binding{Name: bind.Symbol.Name, Value: lowerExpr(bind.Value)})
		}
		for _, ret := range typedFn.Returns {
			fn.Returns = append(fn.Returns, lowerExpr(ret.Value))
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
