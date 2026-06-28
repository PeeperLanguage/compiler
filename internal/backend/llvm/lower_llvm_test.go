package llvm

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/source"
	"compiler/pkg/peeper"
)

const (
	unixTestPath    = "/tmp/test" + peeper.SourceExt
	windowsTestPath = `C:\tmp\test` + peeper.SourceExt
)

func TestLLVMTypeNameModelTypes(t *testing.T) {
	cases := map[string]string{
		"string":           "{ i8*, i64 }",
		"?i32":             "{ i1, i32 }",
		"?string":          "{ i1, { i8*, i64 } }",
		"?^i32":            "i32*",
		"?^const i32":      "i32*",
		"[4]i32":           "[4 x i32]",
		"[]i32":            "{ i32*, i64 }",
		"^const string":    "{ i8*, i64 }*",
		"[]?string":        "{ { i1, { i8*, i64 } }*, i64 }",
		"struct{x: [2]u8}": "{ [2 x i8] }",
	}
	for typeText, want := range cases {
		got, ok := llvmTypeName(typeText)
		if !ok {
			t.Fatalf("llvmTypeName(%q) was rejected", typeText)
		}
		if got != want {
			t.Fatalf("llvmTypeName(%q) = %q, want %q", typeText, got, want)
		}
	}
}

func TestOptionalNicheLayout(t *testing.T) {
	niche, ok := optionalNicheLayout("^const i32")
	if !ok {
		t.Fatalf("expected optional pointer niche")
	}
	if niche.llvmType != "i32*" || niche.none != "zeroinitializer" {
		t.Fatalf("unexpected niche layout: %#v", niche)
	}
	if _, ok := optionalNicheLayout("i32"); ok {
		t.Fatalf("plain integer must not use niche layout without invalid value rule")
	}
}

func TestGenerateLLVMIRLowersZeroValueOptionals(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "tagged",
				ReturnType: "?i32",
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "x", Value: &mir.ZeroValue{Type: "?i32"}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "x", Type: "?i32"}},
				}},
			},
			{
				Name:       "niche",
				ReturnType: "?^i32",
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "p", Value: &mir.ZeroValue{Type: "?^i32"}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "p", Type: "?^i32"}},
				}},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "define { i1, i32 } @tagged(") {
		t.Fatalf("expected tagged optional return type, got:\n%s", irText)
	}
	if !strings.Contains(irText, "ret { i1, i32 } zeroinitializer") {
		t.Fatalf("expected tagged optional none as zeroinitializer, got:\n%s", irText)
	}
	if !strings.Contains(irText, "define i32* @niche(") {
		t.Fatalf("expected niche optional pointer return type, got:\n%s", irText)
	}
	if !strings.Contains(irText, "ret i32* zeroinitializer") {
		t.Fatalf("expected niche optional none as pointer zero, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLowersOptionalSome(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "tagged",
				ReturnType: "?i32",
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "x", Value: &mir.OptionalSome{Value: &mir.RefConst{Value: "7", Type: "i32"}, Type: "?i32"}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "x", Type: "?i32"}},
				}},
			},
			{
				Name:       "niche",
				Params:     []ir.Param{{Name: "p", Type: "^i32"}},
				ReturnType: "?^i32",
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "x", Value: &mir.OptionalSome{Value: &mir.RefName{Name: "p", Type: "^i32"}, Type: "?^i32"}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "x", Type: "?^i32"}},
				}},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "insertvalue { i1, i32 } zeroinitializer, i1 true, 0") {
		t.Fatalf("expected tagged optional some discriminant, got:\n%s", irText)
	}
	if !strings.Contains(irText, "insertvalue { i1, i32 } %") || !strings.Contains(irText, "i32 7, 1") {
		t.Fatalf("expected tagged optional payload, got:\n%s", irText)
	}
	if !strings.Contains(irText, "define i32* @niche(i32* %p)") {
		t.Fatalf("expected niche optional pointer ABI, got:\n%s", irText)
	}
	if !strings.Contains(irText, "ret i32* %p") {
		t.Fatalf("expected niche optional some as raw pointer value, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRComparesTaggedOptionalWithNone(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "x", Value: &mir.OptionalSome{Value: &mir.RefConst{Value: "7", Type: "i32"}, Type: "?i32"}},
						&mir.Assign{Name: "none", Value: &mir.ZeroValue{Type: "?i32"}},
						&mir.Assign{Name: "isnone", Value: &mir.Binary{
							Op:    "==",
							Left:  &mir.RefName{Name: "x", Type: "?i32"},
							Right: &mir.RefName{Name: "none", Type: "?i32"},
							Type:  "bool",
						}},
					},
					Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
				}},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "extractvalue { i1, i32 } %") {
		t.Fatalf("expected optional tag extraction, got:\n%s", irText)
	}
	if !strings.Contains(irText, "icmp eq i1") {
		t.Fatalf("expected tag compare against none, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLoopMutationUsesStackSlot(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "n", Value: &mir.Move{Src: &mir.RefConst{Value: "0", Type: "i32"}, Type: "i32"}},
						},
						Term: &mir.Jump{TargetID: 1},
					},
					{
						ID: 1,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "cond", Value: &mir.Binary{
								Op:    "<",
								Left:  &mir.RefName{Name: "n", Type: "i32"},
								Right: &mir.RefConst{Value: "3", Type: "i32"},
								Type:  "bool",
							}},
						},
						Term: &mir.Branch{Cond: &mir.RefName{Name: "cond", Type: "bool"}, ThenID: 2, ElseID: 3},
					},
					{
						ID: 2,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "next", Value: &mir.Binary{
								Op:    "+",
								Left:  &mir.RefName{Name: "n", Type: "i32"},
								Right: &mir.RefConst{Value: "1", Type: "i32"},
								Type:  "i32",
							}},
							&mir.Assign{Name: "n", Value: &mir.Move{Src: &mir.RefName{Name: "next", Type: "i32"}, Type: "i32"}},
						},
						Term: &mir.Jump{TargetID: 1},
					},
					{
						ID:   3,
						Term: &mir.Ret{Value: &mir.RefName{Name: "n", Type: "i32"}},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "alloca i32") {
		t.Fatalf("expected stack slot for loop-mutated local, got:\n%s", irText)
	}
	if strings.Contains(irText, "ret i32 %next") {
		t.Fatalf("expected return to load loop-mutated local, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRVoidMainUsesIntExitABI(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "void",
				EntryID:    0,
				Location:   source.NewLocation(unixTestPath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "t1", Value: &mir.Call{
								Callee: &mir.RefName{Name: "write", Type: "fn() -> i32"},
								Type:   "i32",
							}, Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 12})},
						},
						Term: &mir.Ret{Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 8})},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
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
		FilePath: windowsTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation(windowsTestPath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Call{
								Callee:   &mir.RefName{Name: "Ping", Type: "fn() -> void"},
								Type:     "void",
								Location: source.NewLocation(windowsTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 8}),
							},
						},
						Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}, Location: source.NewLocation(windowsTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 10})},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "windows")
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
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation(unixTestPath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Call{
								Callee:   &mir.RefName{Name: "Ping", Type: "fn() -> void"},
								Type:     "void",
								Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 8}),
							},
						},
						Term: &mir.Ret{
							Value:    &mir.RefConst{Value: "0", Type: "i32"},
							Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 10}),
						},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, true, "linux")
	if !strings.Contains(irText, "!llvm.dbg.cu") {
		t.Fatalf("expected debug compile unit metadata, got:\n%s", irText)
	}
	if !strings.Contains(irText, "define i32 @main() !dbg !") {
		t.Fatalf("expected debug-tagged function definition, got:\n%s", irText)
	}
	if !strings.Contains(irText, "call void @Ping(), !dbg !") {
		t.Fatalf("expected instruction debug location, got:\n%s", irText)
	}
	if !strings.Contains(irText, `!DIFile(filename: "test`+peeper.SourceExt+`", directory: "/tmp")`) {
		t.Fatalf("expected source file metadata, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRDebugMetadataPreservesNestedExpressionLines(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation(unixTestPath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{
								Name: "t1",
								Value: &mir.Binary{
									Op:       "+",
									Left:     &mir.RefConst{Value: "1", Type: "i32", Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 3})},
									Right:    &mir.RefConst{Value: "2", Type: "i32", Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     "i32",
									Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
								},
								Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
							},
							&mir.Assign{
								Name: "t2",
								Value: &mir.Binary{
									Op:       "*",
									Left:     &mir.RefName{Name: "t1", Type: "i32", Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7})},
									Right:    &mir.RefConst{Value: "3", Type: "i32", Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 3})},
									Type:     "i32",
									Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
								},
								Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
							},
						},
						Term: &mir.Ret{
							Value:    &mir.RefName{Name: "t2", Type: "i32", Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7})},
							Location: source.NewLocation(unixTestPath, source.Position{Line: 4, Column: 2}, source.Position{Line: 4, Column: 8}),
						},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, true, "linux")
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
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: "i32",
				EntryID:    0,
				Location:   source.NewLocation(unixTestPath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{
								Name: "cond",
								Value: &mir.Cast{
									Arg:      &mir.RefConst{Value: "1", Type: "i32", Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     "bool",
									Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 11}),
								},
								Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 11}),
							},
						},
						Term: &mir.Branch{
							Cond:     &mir.RefName{Name: "cond", Type: "bool", Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 11})},
							ThenID:   1,
							ElseID:   2,
							Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 12}),
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

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "icmp ne i32 1, 0") {
		t.Fatalf("expected explicit bool cast to lower as compare, got:\n%s", irText)
	}
	if strings.Contains(irText, "fcmp one") {
		t.Fatalf("unexpected float truthiness compare in integer bool cast, got:\n%s", irText)
	}
}
