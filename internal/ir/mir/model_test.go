package mir

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/hir"
)

func TestGenerateMIRAddsImplicitVoidReturn(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "void",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.ExprStmt{Value: &ir.IntLit{Value: "1", Type: "i32"}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil)
	if out == nil || len(out.Funcs) != 1 {
		t.Fatalf("expected one MIR function, got %#v", out)
	}
	fn := out.Funcs[0]
	if fn == nil || len(fn.Blocks) != 1 {
		t.Fatalf("expected one MIR block, got %#v", fn)
	}
	if _, ok := fn.Blocks[0].Term.(*Ret); !ok {
		t.Fatalf("expected implicit ret terminator, got %#v", fn.Blocks[0].Term)
	}
}
