package llvm

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir/mir"
)

func TestGenerateLLVMIRVoidMainUsesIntExitABI(t *testing.T) {
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

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(""))
	if !strings.Contains(irText, "define i32 @main(") {
		t.Fatalf("expected int main ABI, got:\n%s", irText)
	}
	if !strings.Contains(irText, "ret i32 0") {
		t.Fatalf("expected implicit zero exit status, got:\n%s", irText)
	}
}
