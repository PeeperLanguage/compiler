package place

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

func TestMutableAddressablePreservesProjectionRestrictions(t *testing.T) {
	value := typeinfo.DefaultIntegerType()
	outer := &typeinfo.StructType{}
	owner := &typeinfo.OwnedPtrType{Target: outer}
	shared := &typeinfo.RefType{Target: outer}
	mutable := &typeinfo.RefType{Target: outer, IsMutable: true}
	for _, test := range []struct {
		name        string
		rootType    typeinfo.Type
		childType   typeinfo.Type
		mutableRoot bool
		wantMutable bool
		wantShared  typeinfo.Type
		wantBinding bool
	}{
		{name: "shared to owner", rootType: shared, childType: owner, wantShared: outer},
		{name: "mutable shared binding to owner", rootType: shared, childType: owner, mutableRoot: true, wantShared: outer},
		{name: "shared to mutable reference", rootType: shared, childType: mutable, wantShared: outer},
		{name: "mutable reference to owner", rootType: mutable, childType: owner, wantMutable: true},
		{name: "immutable owner to owner", rootType: owner, childType: owner},
		{name: "mutable owner to owner", rootType: owner, childType: owner, mutableRoot: true, wantMutable: true, wantBinding: true},
		{name: "immutable value to owner", rootType: outer, childType: owner},
		{name: "mutable value to owner", rootType: outer, childType: owner, mutableRoot: true, wantMutable: true, wantBinding: true},
		{name: "immutable owner to value", rootType: owner, childType: value},
		{name: "mutable owner to value", rootType: owner, childType: value, mutableRoot: true, wantMutable: true, wantBinding: true},
		{name: "owner to mutable reference", rootType: owner, childType: mutable, wantMutable: true},
		{name: "owner to shared", rootType: owner, childType: shared, wantShared: outer},
		{name: "shared to value", rootType: shared, childType: value, wantShared: outer},
		{name: "mutable reference to value", rootType: mutable, childType: value, wantMutable: true},
		{name: "immutable value to value", rootType: outer, childType: value},
		{name: "mutable value to value", rootType: outer, childType: value, mutableRoot: true, wantMutable: true, wantBinding: true},
		{name: "raw pointer cutoff", rootType: shared, childType: &typeinfo.RawPtrType{}, wantMutable: true},
	} {
		for _, indexed := range []bool{false, true} {
			name := test.name + "/field"
			if indexed {
				name = test.name + "/index"
			}
			t.Run(name, func(t *testing.T) {
				scope := symbols.NewScope(nil)
				sym := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "outer"), "outer", symbols.SymbolVar, &ast.LetDecl{IsMutable: test.mutableRoot}, nil)
				if err := scope.Declare(sym); err != nil {
					t.Fatal(err)
				}
				root := &ast.Ident{Name: "outer"}
				var child ast.Expr = &ast.SelectorExpr{Expr: root, Name: &ast.Ident{Name: "child"}}
				if indexed {
					child = &ast.IndexExpr{Expr: root, Index: &ast.NumberLit{Value: "0"}}
				}
				field := &ast.SelectorExpr{Expr: child, Name: &ast.Ident{Name: "value"}}
				exprType := func(expr ast.Expr) typeinfo.Type {
					switch expr {
					case root:
						return test.rootType
					case child:
						return test.childType
					default:
						return value
					}
				}
				isMutable, sharedReference, binding := MutableAddressable(scope, field, exprType, nil)
				if isMutable != test.wantMutable || sharedReference != test.wantShared {
					t.Fatalf("MutableAddressable() = (%v, %v), want (%v, %v)", isMutable, sharedReference, test.wantMutable, test.wantShared)
				}
				if test.wantBinding && binding != sym || !test.wantBinding && binding != nil {
					t.Fatalf("mutable binding = %v, want root binding: %v", binding, test.wantBinding)
				}
			})
		}
	}
}
