package typechecker

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/module"
	"compiler/internal/project"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
	"compiler/pkg/peeper"
)

func TestQualifiedBindingBorrowMutability(t *testing.T) {
	for _, test := range []struct {
		name      string
		kind      symbols.Kind
		mutable   bool
		mode      ast.AddressMode
		wantError bool
	}{
		{name: "constant mutable borrow", kind: symbols.SymbolConst, mode: ast.AddressMutable, wantError: true},
		{name: "constant shared borrow", kind: symbols.SymbolConst, mode: ast.AddressShared},
		{name: "constant raw address", kind: symbols.SymbolConst, mode: ast.AddressRaw},
		{name: "immutable mutable borrow", kind: symbols.SymbolVar, mode: ast.AddressMutable, wantError: true},
		{name: "mutable mutable borrow", kind: symbols.SymbolVar, mutable: true, mode: ast.AddressMutable},
	} {
		t.Run(test.name, func(t *testing.T) {
			diag := diagnostics.NewDiagnosticBag()
			mod := &module.Module{SymbolIndex: symbols.NewIndex()}
			c := &checker{ctx: project.New(".", peeper.SourceExt, diag), module: mod, evidence: newEvidence()}
			path := &ast.ScopeResolution{
				NodeIDHolder: ast.NodeIDHolder{NodeID: source.ParsedNodeID(1)},
				Segments:     []ast.PathSegment{{Name: &ast.Ident{Name: "external"}}, {Name: &ast.Ident{Name: "value"}}},
			}
			bound := symbols.New(symbols.ProjectedSymbolID(test.kind, "value"), "value", test.kind, &ast.LetDecl{IsMutable: test.mutable}, nil)
			bound.BindType(typeinfo.DefaultIntegerType())
			mod.SymbolIndex.Bind(path, bound)
			scope := symbols.NewScope(nil)
			shadow := symbols.New(symbols.ProjectedSymbolID(symbols.SymbolVar, "shadow"), "value", symbols.SymbolVar, &ast.LetDecl{IsMutable: !test.mutable}, nil)
			if err := scope.Declare(shadow); err != nil {
				t.Fatal(err)
			}
			borrow := &ast.AddressExpr{NodeIDHolder: ast.NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Expr: path, Mode: test.mode}
			c.typeExpr(scope, borrow, nil)
			if got := hasTypeCode(diag, diagnostics.ErrInvalidExpression); got != test.wantError {
				t.Fatalf("invalid expression = %v, want %v; diagnostics:\n%s", got, test.wantError, diag.EmitAllToString())
			}
			if !test.wantError && diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
			if test.mutable && !mod.SymbolIndex.RequiresMutable(bound) {
				t.Fatal("mutable borrow did not mark resolved binding mutable-required")
			}
			if !test.wantError && c.temporaryBorrowSource(scope, borrow) != nil {
				t.Fatal("qualified binding classified as borrowed temporary")
			}
		})
	}
}
