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
		FilePath: "/tmp/test.em",
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "void",
				EntryID:    0,
				Location:   source.NewLocation("/tmp/test.em", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "t1", Value: &mir.Call{
								Callee: &mir.RefName{Name: "write", Type: "fn() -> i32"},
								Type:   "i32",
							}, Location: source.NewLocation("/tmp/test.em", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 12})},
						},
						Term: &mir.Ret{Location: source.NewLocation("/tmp/test.em", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 8})},
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
		FilePath: `C:\tmp\test.em`,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation(`C:\tmp\test.em`, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Call{
								Callee:   &mir.RefName{Name: "Ping", Type: "fn() -> void"},
								Type:     "void",
								Location: source.NewLocation(`C:\tmp\test.em`, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 8}),
							},
						},
						Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}, Location: source.NewLocation(`C:\tmp\test.em`, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 10})},
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
		FilePath: "/tmp/test.em",
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation("/tmp/test.em", source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Call{
								Callee:   &mir.RefName{Name: "Ping", Type: "fn() -> void"},
								Type:     "void",
								Location: source.NewLocation("/tmp/test.em", source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 8}),
							},
						},
						Term: &mir.Ret{
							Value:    &mir.RefConst{Value: "0", Type: "i32"},
							Location: source.NewLocation("/tmp/test.em", source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 10}),
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
	if !strings.Contains(irText, `!DIFile(filename: "test.em", directory: "/tmp")`) {
		t.Fatalf("expected source file metadata, got:\n%s", irText)
	}
}
