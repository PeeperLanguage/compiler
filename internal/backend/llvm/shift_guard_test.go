package llvm

import (
	"fmt"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
)

func TestGenerateLLVMIRShiftGuardWidths(t *testing.T) {
	for _, target := range []struct {
		name string
		bits int
	}{
		{name: "amd64", bits: 64},
		{name: "386", bits: 32},
	} {
		for _, tt := range []struct {
			name        string
			leftBits    int
			countBits   int
			leftSigned  bool
			countSigned bool
		}{
			{name: "narrow unsigned", leftBits: 32, countBits: 5},
			{name: "narrow signed", leftBits: 32, countBits: 5, countSigned: true},
			{name: "signed operand", leftBits: 32, countBits: 5, leftSigned: true},
			{name: "one bit", leftBits: 1, countBits: 1},
			{name: "two bits signed count", leftBits: 2, countBits: 1, countSigned: true},
			{name: "odd width", leftBits: 3, countBits: 2},
			{name: "wide operand", leftBits: 128, countBits: 5},
			{name: "wide unsigned count", leftBits: 8, countBits: 64},
			{name: "wide signed count", leftBits: 8, countBits: 64, countSigned: true},
			{name: "equal width signed count", leftBits: 32, countBits: 32, countSigned: true},
		} {
			for _, op := range []string{"<<", ">>"} {
				t.Run(target.name+"/"+tt.name+"/"+op, func(t *testing.T) {
					types := newLLVMTypeFixture(target.bits)
					leftType := types.table.Intern(ir.Type{Kind: ir.TypeInteger, Bits: tt.leftBits, IsSigned: tt.leftSigned})
					countType := types.table.Intern(ir.Type{Kind: ir.TypeInteger, Bits: tt.countBits, IsSigned: tt.countSigned})
					mod := &mir.Module{
						Name: "test", Types: types.table,
						Funcs: []*mir.Function{{
							Name:       "apply",
							Params:     []ir.Param{{Name: "left", Type: leftType}, {Name: "right", Type: countType}},
							ReturnType: leftType,
							Blocks: []*mir.Block{{
								ID: 0,
								Instrs: []mir.Instr{&mir.Assign{Name: "result", Value: &mir.Binary{
									Op: op, Left: &mir.RefName{Name: "left", Type: leftType}, Right: &mir.RefName{Name: "right", Type: countType}, Type: leftType,
								}}},
								Term: &mir.Ret{Value: &mir.RefName{Name: "result", Type: leftType}},
							}},
						}},
					}
					targetInfo := testLinuxAMD64
					if target.bits == 32 {
						targetInfo = testLinux386
					}
					diag := diagnostics.NewDiagnosticBag()
					out := GenerateLLVMIR(mod, diag, targetInfo, false)
					if diag.HasErrors() || out == "" {
						t.Fatalf("shift lowering failed:\n%s", out)
					}

					count := "%right"
					if tt.countBits < tt.leftBits {
						extension := "zext"
						if tt.countSigned {
							extension = "sext"
						}
						cast := fmt.Sprintf(" = %s i%d %%right to i%d", extension, tt.countBits, tt.leftBits)
						if strings.Count(out, cast) != 1 {
							t.Fatalf("expected one lossless count extension %q:\n%s", cast, out)
						}
						before, _, _ := strings.Cut(out, cast)
						count = strings.TrimSpace(before[strings.LastIndex(before, "\n")+1:])
					}
					guardText := fmt.Sprintf("icmp uge i%d %s, %d", max(tt.leftBits, tt.countBits), count, tt.leftBits)
					guard := strings.Index(out, guardText)
					trap := strings.Index(out, "call void @llvm.trap()")
					ready := strings.Index(out, "\nshift_ready_")
					opcode := "shl"
					if op == ">>" {
						opcode = "lshr"
						if tt.leftSigned {
							opcode = "ashr"
						}
					}
					shift := strings.Index(out, fmt.Sprintf(" = %s i%d %%left,", opcode, tt.leftBits))
					if guard < 0 || trap < guard || ready < trap || shift < ready {
						t.Fatalf("expected guard %q before trap/ready/shift:\n%s", guardText, out)
					}
					if tt.countBits > tt.leftBits {
						cast := strings.Index(out, fmt.Sprintf("trunc i%d %%right to i%d", tt.countBits, tt.leftBits))
						if cast < ready || shift < cast {
							t.Fatalf("wide count must only truncate after guard succeeds:\n%s", out)
						}
					} else if !strings.Contains(out, fmt.Sprintf(" = %s i%d %%left, %s", opcode, tt.leftBits, count)) {
						t.Fatalf("shift must reuse validated count:\n%s", out)
					}
				})
			}
		}
	}
}
