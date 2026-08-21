package fold

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/symbols"
)

func TestApplyConstantFoldingPreservesReturnCleanup(t *testing.T) {
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	ownedI32 := types.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: i32})
	mod := &hir.Module{Funcs: []*hir.Function{{
		Name: "main",
		Body: &hir.Block{Stmts: []hir.Stmt{&hir.Return{
			Value: &ir.IntLit{Value: "0", Type: i32},
			Cleanup: []ir.Expr{&ir.Drop{Value: &ir.Ident{
				Name: "owner",
				Type: ownedI32,
			}}},
		}}},
	}}, Types: types}

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
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	arrayI32 := types.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32, Length: "2"})
	rawptr := types.Intern(ir.Type{Kind: ir.TypeRawPtr})
	mod := &hir.Module{Funcs: []*hir.Function{{
		Name: "main",
		Body: &hir.Block{Stmts: []hir.Stmt{
			&hir.Binding{Name: "value", Constant: true, Value: &ir.IntLit{Value: "7", Type: i32}},
			&hir.Binding{Name: "index", Constant: true, Value: &ir.IntLit{Value: "1", Type: i32}},
			&hir.Binding{Name: "address", Value: &ir.AddrOf{
				Place: &ir.Place{Root: &ir.Ident{Name: "value", Type: i32}, Type: i32},
				Type:  rawptr,
			}},
			&hir.Binding{Name: "item", Value: &ir.Load{Place: &ir.Place{
				Root: &ir.Ident{Name: "items", Type: arrayI32},
				Projections: []ir.PlaceProjection{{
					Kind: ir.PlaceProjectionIndex, Index: &ir.Ident{Name: "index", Type: i32}, Type: i32,
				}},
				Type: i32,
			}}},
		}},
	}}, Types: types}

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

func TestApplyConstantFoldingPreservesHIRIdentity(t *testing.T) {
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	mod := &hir.Module{Funcs: []*hir.Function{{
		Name:     "main",
		NodeID:   11,
		SymbolID: symbols.SymbolID(12),
		Body: &hir.Block{NodeID: 13, Stmts: []hir.Stmt{
			&hir.Binding{Name: "value", Constant: true, Value: &ir.IntLit{Value: "7", Type: i32}, NodeID: 14, SymbolID: symbols.SymbolID(15)},
			&hir.ExprStmt{Value: &ir.IntLit{Value: "7", Type: i32}, NodeID: 16, ValueNodeID: 17},
		}},
	}}, Types: types}

	out := ApplyConstantFolding(mod, nil)
	fn := out.Funcs[0]
	binding := fn.Body.Stmts[0].(*hir.Binding)
	discarded := fn.Body.Stmts[1].(*hir.ExprStmt)
	if fn.NodeID != 11 || fn.SymbolID != 12 || fn.Body.NodeID != 13 || binding.NodeID != 14 || binding.SymbolID != 15 ||
		discarded.NodeID != 16 || discarded.ValueNodeID != 17 {
		t.Fatalf("folded identity = %#v / %#v, want all origin fields preserved", fn, binding)
	}
}
