package llvm

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir/mir"
	"compiler/internal/source"
)

func TestGenerateLLVMIRVoidMainUsesIntExitABI(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: "/tmp/test.peep",
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "void",
				EntryID:    0,
				Location:   source.NewLocation("/tmp/test.peep", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "t1", Value: &mir.Call{
								Callee: &mir.RefName{Name: "write", Type: "fn() -> i32"},
								Type:   "i32",
							}, Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 12})},
						},
						Term: &mir.Ret{Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 8})},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(""), targetTriple, false, "linux")
	if !strings.Contains(irText, "target triple = \""+targetTriple+"\"") {
		t.Fatalf("expected configured target triple, got:\n%s", irText)
	}
	if !strings.Contains(irText, "define i32 @main(") {
		t.Fatalf("expected int main ABI, got:\n%s", irText)
	}
	if !strings.Contains(irText, "ret i32 0") {
		t.Fatalf("expected implicit zero exit status, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRDeclaresDiscardedDirectCall(t *testing.T) {
	const targetTriple = "x86_64-pc-windows-msvc"
	mod := &mir.Module{
		Name:     "test",
		FilePath: `C:\tmp\test.peep`,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation(`C:\tmp\test.peep`, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Call{
								Callee:   &mir.RefName{Name: "Ping", Type: "fn() -> void"},
								Type:     "void",
								Location: source.NewLocation(`C:\tmp\test.peep`, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 8}),
							},
						},
						Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}, Location: source.NewLocation(`C:\tmp\test.peep`, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 10})},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(""), targetTriple, false, "windows")
	if !strings.Contains(irText, "target triple = \""+targetTriple+"\"") {
		t.Fatalf("expected configured target triple, got:\n%s", irText)
	}
	if !strings.Contains(irText, "declare void @Ping()") {
		t.Fatalf("expected declaration for discarded direct call, got:\n%s", irText)
	}
	if !strings.Contains(irText, "call void @Ping()") {
		t.Fatalf("expected emitted discarded direct call, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRDebugMetadata(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: "/tmp/test.peep",
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation("/tmp/test.peep", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Call{
								Callee:   &mir.RefName{Name: "Ping", Type: "fn() -> void"},
								Type:     "void",
								Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 8}),
							},
						},
						Term: &mir.Ret{
							Value:    &mir.RefConst{Value: "0", Type: "i32"},
							Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 10}),
						},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(""), targetTriple, true, "linux")
	if !strings.Contains(irText, "!llvm.dbg.cu") {
		t.Fatalf("expected debug compile unit metadata, got:\n%s", irText)
	}
	if !strings.Contains(irText, "define i32 @main() !dbg !") {
		t.Fatalf("expected debug-tagged function definition, got:\n%s", irText)
	}
	if !strings.Contains(irText, "call void @Ping(), !dbg !") {
		t.Fatalf("expected instruction debug location, got:\n%s", irText)
	}
	if !strings.Contains(irText, `!DIFile(filename: "test.peep", directory: "/tmp")`) {
		t.Fatalf("expected source file metadata, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRDebugMetadataPreservesNestedExpressionLines(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: "/tmp/test.peep",
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation("/tmp/test.peep", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{
								Name: "t1",
								Value: &mir.Binary{
									Op:       "+",
									Left:     &mir.RefConst{Value: "1", Type: "i32", Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 3})},
									Right:    &mir.RefConst{Value: "2", Type: "i32", Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     "i32",
									Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
								},
								Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
							},
							&mir.Assign{
								Name: "t2",
								Value: &mir.Binary{
									Op:       "*",
									Left:     &mir.RefName{Name: "t1", Type: "i32", Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7})},
									Right:    &mir.RefConst{Value: "3", Type: "i32", Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 3})},
									Type:     "i32",
									Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
								},
								Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
							},
						},
						Term: &mir.Ret{
							Value:    &mir.RefName{Name: "t2", Type: "i32", Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7})},
							Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 4, Column: 2}, source.Position{Line: 4, Column: 8}),
						},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(""), targetTriple, true, "linux")
	if !strings.Contains(irText, "!DILocation(line: 2, column: 2") {
		t.Fatalf("expected child expression debug location, got:\n%s", irText)
	}
	if !strings.Contains(irText, "!DILocation(line: 3, column: 2") {
		t.Fatalf("expected parent expression debug location, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRExplicitBoolCastUsesCompare(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: "/tmp/test.peep",
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation("/tmp/test.peep", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{
								Name: "cond",
								Value: &mir.Cast{
									Arg:      &mir.RefConst{Value: "1", Type: "i32", Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     "bool",
									Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 11}),
								},
								Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 11}),
							},
						},
						Term: &mir.Branch{
							Cond:     &mir.RefName{Name: "cond", Type: "bool", Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 11})},
							ThenID:   1,
							ElseID:   2,
							Location: source.NewLocation("/tmp/test.peep", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 12}),
						},
					},
					{
						ID:   1,
						Term: &mir.Ret{Value: &mir.RefConst{Value: "1", Type: "i32"}},
					},
					{
						ID:   2,
						Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(""), targetTriple, false, "linux")
	if !strings.Contains(irText, "icmp ne i32 1, 0") {
		t.Fatalf("expected explicit bool cast to lower as compare, got:\n%s", irText)
	}
	if strings.Contains(irText, "fcmp one") {
		t.Fatalf("unexpected float truthiness compare in integer bool cast, got:\n%s", irText)
	}
}
