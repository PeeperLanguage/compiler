package mir

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/source"
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

func TestGenerateMIRLowersDiscardedValueCallAsPlainCall(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.ExprStmt{
							Value: &ir.Call{
								Callee: &ir.Ident{Name: "Ping", Type: "fn() -> i32"},
								Type:   "i32",
							},
						},
						&hir.Return{Value: &ir.IntLit{Value: "0", Type: "i32"}},
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
	if len(fn.Blocks[0].Instrs) != 1 {
		t.Fatalf("expected one MIR instruction, got %#v", fn.Blocks[0].Instrs)
	}
	call, ok := fn.Blocks[0].Instrs[0].(*Call)
	if !ok {
		t.Fatalf("expected plain call instruction, got %#v", fn.Blocks[0].Instrs[0])
	}
	if call.Type != "i32" {
		t.Fatalf("expected preserved call return type, got %q", call.Type)
	}
}

func TestGenerateMIRPreservesNestedExpressionLocations(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{
							Value: &ir.Binary{
								Op: "*",
								Left: &ir.Binary{
									Op:       "+",
									Left:     &ir.IntLit{Value: "1", Type: "i32", Location: source.NewLocation("test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 3})},
									Right:    &ir.IntLit{Value: "2", Type: "i32", Location: source.NewLocation("test.peep", source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     "i32",
									Location: source.NewLocation("test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
								},
								Right:    &ir.IntLit{Value: "3", Type: "i32", Location: source.NewLocation("test.peep", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 3})},
								Type:     "i32",
								Location: source.NewLocation("test.peep", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
							},
							Location: source.NewLocation("test.peep", source.Position{Line: 4, Column: 2}, source.Position{Line: 4, Column: 8}),
						},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	instrs := out.Funcs[0].Blocks[0].Instrs
	if len(instrs) != 2 {
		t.Fatalf("expected two lowered binary instructions, got %#v", instrs)
	}
	first, ok := instrs[0].(*Assign)
	if !ok || first.Location == nil || first.Location.Start == nil || first.Location.Start.Line != 2 {
		t.Fatalf("expected child expression location on first assign, got %#v", instrs[0])
	}
	second, ok := instrs[1].(*Assign)
	if !ok || second.Location == nil || second.Location.Start == nil || second.Location.Start.Line != 3 {
		t.Fatalf("expected parent expression location on second assign, got %#v", instrs[1])
	}
}
