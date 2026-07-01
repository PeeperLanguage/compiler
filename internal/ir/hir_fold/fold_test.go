package hir_fold

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/source"
)

func TestApplyConstantFoldingReportsArrayIndexOutOfBoundsWithLocation(t *testing.T) {
	indexLoc := source.NewLocation("test.peep", source.Position{Line: 3, Column: 13}, source.Position{Line: 3, Column: 18})
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.Index{
							Base: &ir.Ident{Name: "arr", Type: "[3]i32"},
							Index: &ir.Binary{
								Op:       "+",
								Left:     &ir.IntLit{Value: "1", Type: "i32"},
								Right:    &ir.IntLit{Value: "2", Type: "i32"},
								Type:     "i32",
								Location: indexLoc,
							},
							Type: "i32",
						}},
					},
				},
			},
		},
	}
	diag := diagnostics.NewDiagnosticBag()

	ApplyConstantFolding(mod, diag)

	items := diag.Diagnostics()
	if len(items) != 1 {
		t.Fatalf("expected one diagnostic, got %#v", items)
	}
	item := items[0]
	if item.Code != diagnostics.ErrArrayOutOfBounds {
		t.Fatalf("expected array OOB diagnostic, got %#v", item)
	}
	if len(item.Labels) != 1 || item.Labels[0].Location == nil || item.Labels[0].Location.Start == nil {
		t.Fatalf("expected source label, got %#v", item.Labels)
	}
	if got := item.Labels[0].Location.Start; got.Line != 3 || got.Column != 13 {
		t.Fatalf("label start = %d:%d, want 3:13", got.Line, got.Column)
	}
}
