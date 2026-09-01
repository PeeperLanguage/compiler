package fold

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/symbols"
)

func TestApplyTypedExpressionFoldingPreservesConstantBranches(t *testing.T) {
	types := ir.NewTypeTable()
	boolType := types.Intern(ir.Type{Kind: ir.TypeBool})
	mod := &hir.Module{Funcs: []*hir.Function{{
		Name: "main",
		Body: &hir.Block{Stmts: []hir.Stmt{&hir.If{
			Cond:   &ir.BoolLit{Value: false, Type: boolType},
			Then:   &hir.Block{NodeID: 11, Stmts: []hir.Stmt{&hir.Return{NodeID: 12}}},
			Else:   &hir.Block{NodeID: 13, Stmts: []hir.Stmt{&hir.Return{NodeID: 14}}},
			NodeID: 10,
		}}},
	}}, Types: types}

	out := ApplyTypedExpressionFolding(mod)
	if len(out.Funcs[0].Body.Stmts) != 1 {
		t.Fatalf("folded statements = %#v, want one if", out.Funcs[0].Body.Stmts)
	}
	branch, ok := out.Funcs[0].Body.Stmts[0].(*hir.If)
	if !ok || branch.NodeID != 10 || branch.Then == nil || branch.Then.NodeID != 11 || branch.Else == nil || hir.NodeIDOf(branch.Else) != 13 {
		t.Fatalf("folded branch = %#v, want preserved true and false branches", out.Funcs[0].Body.Stmts[0])
	}
	if cond, ok := branch.Cond.(*ir.BoolLit); !ok || cond.Value {
		t.Fatalf("folded condition = %#v, want false literal", branch.Cond)
	}
}

func TestApplyTypedExpressionFoldingFoldsAllForSegments(t *testing.T) {
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	add := func(left, right string) ir.Expr {
		return &ir.Binary{Op: "+", Left: &ir.IntLit{Value: left, Type: i32}, Right: &ir.IntLit{Value: right, Type: i32}, Type: i32}
	}
	loop := &hir.For{
		Init:     &hir.Block{NodeID: 2, Stmts: []hir.Stmt{&hir.Binding{Value: add("1", "1")}}},
		Cond:     &ir.Binary{Op: "<", Left: add("1", "1"), Right: &ir.IntLit{Value: "3", Type: i32}, Type: types.Intern(ir.Type{Kind: ir.TypeBool})},
		Bindings: &hir.Block{NodeID: 3, Stmts: []hir.Stmt{&hir.Binding{Value: add("2", "2")}}},
		Body:     &hir.Block{NodeID: 4, Stmts: []hir.Stmt{&hir.ExprStmt{Value: add("3", "3")}}},
		Next:     &hir.Block{NodeID: 5, Stmts: []hir.Stmt{&hir.Assign{Target: &ir.Place{Root: &ir.Ident{Name: "cursor", Type: i32}, Type: i32}, Value: add("4", "4")}}},
		NodeID:   1,
	}
	mod := &hir.Module{Types: types, Funcs: []*hir.Function{{Name: "main", Body: &hir.Block{Stmts: []hir.Stmt{loop}}}}}

	out := ApplyTypedExpressionFolding(mod)
	folded := out.Funcs[0].Body.Stmts[0].(*hir.For)
	values := []ir.Expr{
		folded.Init.Stmts[0].(*hir.Binding).Value,
		folded.Bindings.Stmts[0].(*hir.Binding).Value,
		folded.Body.Stmts[0].(*hir.ExprStmt).Value,
		folded.Next.Stmts[0].(*hir.Assign).Value,
	}
	for index, value := range values {
		literal, ok := value.(*ir.IntLit)
		if !ok || literal.Value != []string{"2", "4", "6", "8"}[index] {
			t.Fatalf("folded segment %d = %#v", index, value)
		}
	}
	if folded.Init.NodeID != 2 || folded.Bindings.NodeID != 3 || folded.Body.NodeID != 4 || folded.Next.NodeID != 5 {
		t.Fatalf("folded loop segment identities = %#v", folded)
	}
}

func TestApplyTypedExpressionFoldingPreservesStatementsAfterReturn(t *testing.T) {
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	mod := &hir.Module{Funcs: []*hir.Function{{
		Name: "main",
		Body: &hir.Block{Stmts: []hir.Stmt{
			&hir.Return{Value: &ir.IntLit{Value: "0", Type: i32}, NodeID: 20},
			&hir.ExprStmt{Value: &ir.IntLit{Value: "1", Type: i32}, NodeID: 21},
		}},
	}}, Types: types}

	out := ApplyTypedExpressionFolding(mod)
	if len(out.Funcs[0].Body.Stmts) != 2 || hir.NodeIDOf(out.Funcs[0].Body.Stmts[1]) != 21 {
		t.Fatalf("folded statements = %#v, want source statement after return preserved", out.Funcs[0].Body.Stmts)
	}
}

func TestApplyTypedExpressionFoldingPreservesReturnCleanup(t *testing.T) {
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

	out := ApplyTypedExpressionFolding(mod)
	ret, ok := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	if !ok || len(ret.Cleanup) != 1 {
		t.Fatalf("folded return cleanup = %#v, want one expression", ret)
	}
	if _, ok := ret.Cleanup[0].(*ir.Drop); !ok {
		t.Fatalf("folded cleanup = %#v, want drop", ret.Cleanup[0])
	}
}

func TestApplyTypedExpressionFoldingPreservesPlaceRootAndFoldsIndexes(t *testing.T) {
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

	out := ApplyTypedExpressionFolding(mod)
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

func TestApplyTypedExpressionFoldingFoldsAssignments(t *testing.T) {
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	arrayI32 := types.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32, Length: "2"})
	mod := &hir.Module{Funcs: []*hir.Function{{
		Name: "main",
		Body: &hir.Block{Stmts: []hir.Stmt{
			&hir.Binding{Name: "index", Constant: true, Value: &ir.IntLit{Value: "1", Type: i32}},
			&hir.Assign{
				Target: &ir.Place{
					Root: &ir.Ident{Name: "items", Type: arrayI32},
					Projections: []ir.PlaceProjection{{
						Kind: ir.PlaceProjectionIndex, Index: &ir.Ident{Name: "index", Type: i32}, Type: i32,
					}},
					Type: i32,
				},
				Value:      &ir.Binary{Op: "+", Left: &ir.IntLit{Value: "20", Type: i32}, Right: &ir.IntLit{Value: "22", Type: i32}, Type: i32},
				DropTarget: true,
				NodeID:     31,
			},
		}},
	}}, Types: types}

	out := ApplyTypedExpressionFolding(mod)
	assignment := out.Funcs[0].Body.Stmts[1].(*hir.Assign)
	root, rootOK := assignment.Target.Root.(*ir.Ident)
	index, indexOK := assignment.Target.Projections[0].Index.(*ir.IntLit)
	value, valueOK := assignment.Value.(*ir.IntLit)
	if !rootOK || root.Name != "items" || !indexOK || index.Value != "1" || !valueOK || value.Value != "42" ||
		!assignment.DropTarget || assignment.NodeID != 31 {
		t.Fatalf("folded assignment = %#v, want preserved target with folded index and value", assignment)
	}
}

func TestApplyTypedExpressionFoldingPreservesHIRIdentity(t *testing.T) {
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	mod := &hir.Module{Funcs: []*hir.Function{{
		Name:     "main",
		NodeID:   11,
		SymbolID: symbols.SymbolID(12),
		Body: &hir.Block{NodeID: 13, Stmts: []hir.Stmt{
			&hir.Binding{Name: "value", Constant: true, Type: i32, Value: &ir.IntLit{Value: "7", Type: i32}, NodeID: 14, SymbolID: symbols.SymbolID(15)},
			&hir.ExprStmt{Value: &ir.IntLit{Value: "7", Type: i32}, NodeID: 16, ValueNodeID: 17},
		}},
	}}, Types: types}

	out := ApplyTypedExpressionFolding(mod)
	fn := out.Funcs[0]
	binding := fn.Body.Stmts[0].(*hir.Binding)
	discarded := fn.Body.Stmts[1].(*hir.ExprStmt)
	if fn.NodeID != 11 || fn.SymbolID != 12 || fn.Body.NodeID != 13 || binding.NodeID != 14 || binding.SymbolID != 15 || binding.Type != i32 ||
		discarded.NodeID != 16 || discarded.ValueNodeID != 17 {
		t.Fatalf("folded identity = %#v / %#v, want all origin fields preserved", fn, binding)
	}
}

func TestApplyTypedExpressionFoldingPreservesVariantSwitchBindings(t *testing.T) {
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	switchStmt := &hir.SwitchVariant{
		Value: &ir.Binary{Op: "+", Left: &ir.IntLit{Value: "20", Type: i32}, Right: &ir.IntLit{Value: "22", Type: i32}, Type: i32},
		Cases: []hir.VariantCaseBlock{{
			Case: 1, PayloadType: i32,
			Bindings: []hir.VariantBinding{{FieldIndex: 2, Name: "payload", Type: i32, SymbolID: 9}},
			Body: &hir.Block{NodeID: 12, Stmts: []hir.Stmt{&hir.Return{
				Value: &ir.Binary{Op: "+", Left: &ir.IntLit{Value: "1", Type: i32}, Right: &ir.IntLit{Value: "2", Type: i32}, Type: i32},
			}}},
		}},
		NodeID: 10,
	}
	mod := &hir.Module{Types: types, Funcs: []*hir.Function{{Name: "main", Body: &hir.Block{Stmts: []hir.Stmt{switchStmt}}}}}

	out := ApplyTypedExpressionFolding(mod)
	folded, ok := out.Funcs[0].Body.Stmts[0].(*hir.SwitchVariant)
	if !ok || folded.NodeID != 10 || len(folded.Cases) != 1 || len(folded.Cases[0].Bindings) != 1 {
		t.Fatalf("folded switch = %#v", out.Funcs[0].Body.Stmts[0])
	}
	if value, ok := folded.Value.(*ir.IntLit); !ok || value.Value != "42" {
		t.Fatalf("folded subject = %#v, want 42", folded.Value)
	}
	binding := folded.Cases[0].Bindings[0]
	if binding.FieldIndex != 2 || binding.SymbolID != 9 || binding.Type != i32 || binding.Name != "payload" {
		t.Fatalf("folded pattern binding = %#v", binding)
	}
	ret := folded.Cases[0].Body.Stmts[0].(*hir.Return)
	if value, ok := ret.Value.(*ir.IntLit); !ok || value.Value != "3" {
		t.Fatalf("folded case return = %#v, want 3", ret.Value)
	}
}
