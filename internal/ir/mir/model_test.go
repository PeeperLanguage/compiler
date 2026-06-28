package mir

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/source"
	"compiler/pkg/peeper"
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

func TestGenerateMIRLowersZeroValue(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "maybe",
				ReturnType: "?i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.ZeroValue{Type: "?i32"}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	block := out.Funcs[0].Blocks[0]
	if len(block.Instrs) != 1 {
		t.Fatalf("expected zero value assign, got %#v", block.Instrs)
	}
	assign, ok := block.Instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected assign, got %#v", block.Instrs[0])
	}
	zero, ok := assign.Value.(*ZeroValue)
	if !ok || zero.Type != "?i32" {
		t.Fatalf("expected ?i32 zero value, got %#v", assign.Value)
	}
}

func TestGenerateMIRLowersOptionalSome(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "maybe",
				ReturnType: "?i32",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.Return{Value: &ir.OptionalSome{Value: &ir.IntLit{Value: "7", Type: "i32"}, Type: "?i32"}},
					},
				},
			},
		},
	}

	out := GenerateMIR(mod, nil)
	if out == nil || len(out.Funcs) != 1 || len(out.Funcs[0].Blocks) != 1 {
		t.Fatalf("unexpected MIR shape: %#v", out)
	}
	block := out.Funcs[0].Blocks[0]
	if len(block.Instrs) != 1 {
		t.Fatalf("expected optional some assign, got %#v", block.Instrs)
	}
	assign, ok := block.Instrs[0].(*Assign)
	if !ok {
		t.Fatalf("expected assign, got %#v", block.Instrs[0])
	}
	some, ok := assign.Value.(*OptionalSome)
	if !ok || some.Type != "?i32" {
		t.Fatalf("expected ?i32 optional some, got %#v", assign.Value)
	}
}

func TestGenerateMIRPreservesNestedExpressionLocations(t *testing.T) {
	testPath := "test" + peeper.SourceExt
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
									Left:     &ir.IntLit{Value: "1", Type: "i32", Location: source.NewLocation(testPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 3})},
									Right:    &ir.IntLit{Value: "2", Type: "i32", Location: source.NewLocation(testPath, source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     "i32",
									Location: source.NewLocation(testPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
								},
								Right:    &ir.IntLit{Value: "3", Type: "i32", Location: source.NewLocation(testPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 3})},
								Type:     "i32",
								Location: source.NewLocation(testPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
							},
							Location: source.NewLocation(testPath, source.Position{Line: 4, Column: 2}, source.Position{Line: 4, Column: 8}),
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

func TestGenerateMIRLowersForLoop(t *testing.T) {
	mod := &hir.Module{
		Name: "test",
		Funcs: []*hir.Function{
			{
				Name:       "main",
				ReturnType: "void",
				Body: &hir.Block{
					Stmts: []hir.Stmt{
						&hir.For{
							Cond: &ir.IntLit{Value: "1", Type: "bool"},
							Body: &hir.Block{
								Stmts: []hir.Stmt{
									&hir.ExprStmt{
										Value: &ir.Call{
											Callee: &ir.Ident{Name: "Ping", Type: "fn() -> i32"},
											Type:   "i32",
										},
									},
								},
							},
						},
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
	if len(fn.Blocks) != 4 {
		t.Fatalf("expected four blocks for loop, got %#v", fn.Blocks)
	}
	entry := fn.Blocks[0]
	entryJump, ok := entry.Term.(*Jump)
	if !ok {
		t.Fatalf("expected entry jump terminator, got %#v", entry.Term)
	}
	header := fn.Blocks[1]
	if entryJump.TargetID != header.ID {
		t.Fatalf("expected jump to loop header, got %#v", entry.Term)
	}
	term, ok := header.Term.(*Branch)
	if !ok {
		t.Fatalf("expected header branch terminator, got %#v", header.Term)
	}
	if term.ThenID != fn.Blocks[2].ID || term.ElseID != fn.Blocks[3].ID {
		t.Fatalf("unexpected loop targets: %#v", term)
	}
	bodyTerm, ok := fn.Blocks[2].Term.(*Jump)
	if !ok || bodyTerm.TargetID != header.ID {
		t.Fatalf("expected backedge to header block, got %#v", fn.Blocks[2].Term)
	}
}
