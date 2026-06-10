package llvm

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir/mir"
)

func TestGenerateLLVMIRVoidMainUsesIntExitABI(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "void",
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "t1", Value: &mir.Call{
								Callee: &mir.RefName{Name: "write", Type: "fn() -> i32"},
								Type:   "i32",
							}},
						},
						Term: &mir.Ret{},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(""), targetTriple)
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
		Name: "test",
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Call{
								Callee: &mir.RefName{Name: "Ping", Type: "fn() -> void"},
								Type:   "void",
							},
						},
						Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(""), targetTriple)
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
