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

func TestApplyConstantFoldingPreservesPlaceRootAndFoldsIndexes(t *testing.T) {
	mod := &hir.Module{Funcs: []*hir.Function{{
		Name: "main",
		Body: &hir.Block{Stmts: []hir.Stmt{
			&hir.Binding{Name: "value", Constant: true, Value: &ir.IntLit{Value: "7", Type: "i32"}},
			&hir.Binding{Name: "index", Constant: true, Value: &ir.IntLit{Value: "1", Type: "i32"}},
			&hir.Binding{Name: "address", Value: &ir.AddrOf{
				Place: &ir.Place{Root: &ir.Ident{Name: "value", Type: "i32"}, Type: "i32"},
				Type:  "rawptr",
			}},
			&hir.Binding{Name: "item", Value: &ir.Load{Place: &ir.Place{
				Root: &ir.Ident{Name: "items", Type: "[2]i32"},
				Projections: []ir.PlaceProjection{{
					Kind: ir.PlaceProjectionIndex, Index: &ir.Ident{Name: "index", Type: "i32"}, Type: "i32",
				}},
				Type: "i32",
			}}},
		}},
	}}}

	out := ApplyConstantFolding(mod, nil)
	address := out.Funcs[0].Body.Stmts[2].(*hir.Binding).Value.(*ir.AddrOf)
	root, ok := address.Place.Root.(*ir.Ident)
	if !ok || root.Name != "value" {
		t.Fatalf("folded place root = %#v, want value storage identity", address.Place.Root)
	}
	load := out.Funcs[0].Body.Stmts[3].(*hir.Binding).Value.(*ir.Load)
	index, ok := load.Place.Projections[0].Index.(*ir.IntLit)
	if !ok || index.Value != "1" {
		t.Fatalf("folded place index = %#v, want literal 1", load.Place.Projections[0].Index)
	}
}
