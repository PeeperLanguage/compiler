package hir_fold

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/hir"
)

func TestApplyConstantFoldingPreservesReturnCleanup(t *testing.T) {
	mod := &hir.Module{Funcs: []*hir.Function{{
		Name: "main",
		Body: &hir.Block{Stmts: []hir.Stmt{&hir.Return{
			Value: &ir.IntLit{Value: "0", Type: "i32"},
			Cleanup: []ir.Expr{&ir.Drop{Value: &ir.Ident{
				Name: "owner",
				Type: "*i32",
			}}},
		}}},
	}}}

	out := ApplyConstantFolding(mod, nil)
	ret, ok := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	if !ok || len(ret.Cleanup) != 1 {
		t.Fatalf("folded return cleanup = %#v, want one expression", ret)
	}
	if _, ok := ret.Cleanup[0].(*ir.Drop); !ok {
		t.Fatalf("folded cleanup = %#v, want drop", ret.Cleanup[0])
	}
}
