package llvm

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/mir"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
	"compiler/internal/target"
	"compiler/pkg/peeper"
)

const (
	unixTestPath    = "/tmp/test" + peeper.SourceExt
	windowsTestPath = `C:\tmp\test` + peeper.SourceExt
)

func TestLLVMTypeNameModelTypes(t *testing.T) {
	cases := map[string]string{
		"byte":             "i8",
		"i24":              "i24",
		"u8388608":         "i8388608",
		"string":           "{ i8*, i64 }",
		"?i32":             "{ i1, i32 }",
		"?string":          "{ i1, { i8*, i64 } }",
		"?*i32":            "{ i1, { i32*, i8* } }",
		"?*iface{}":        "{ i1, { i8*, i8* } }",
		"*i32":             "{ i32*, i8* }",
		"*iface{}":         "{ i8*, i8* }",
		"rawptr":           "i8*",
		"[4]i32":           "[4 x i32]",
		"[]i32":            "{ i32*, i64, i64 }",
		"&i32":             "i32*",
		"&mut i32":         "i32*",
		"&[]i32":           "{ i32*, i64 }",
		"&mut []i32":       "{ i32*, i64 }",
		"*string":          "{ { i8*, i64 }*, i8* }",
		"[]?string":        "{ { i1, { i8*, i64 } }*, i64, i64 }",
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

func TestLLVMFloatConstantsUseWidthCorrectHex(t *testing.T) {
	f32 := llvmFloatConst("2.4", "f32")
	f64 := llvmFloatConst("2.4", "f64")
	if !strings.HasPrefix(f32, "0x") || !strings.HasPrefix(f64, "0x") || f32 == f64 {
		t.Fatalf("float constants: f32=%q f64=%q", f32, f64)
	}
}

func TestGenerateLLVMIRLowersBooleanCallArguments(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{
			{Name: "accept", Params: []ir.Param{{Name: "value", Type: "bool"}}, ReturnType: "void"},
			{
				Name:       "main",
				ReturnType: "i32",
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Call{
							Callee: &mir.RefName{Name: "accept", Type: "fn(bool) -> void"},
							Args:   []mir.ValueRef{&mir.RefConst{Value: "true", Type: "bool"}},
							Type:   "void",
						},
						&mir.Call{
							Callee: &mir.RefName{Name: "accept", Type: "fn(bool) -> void"},
							Args:   []mir.ValueRef{&mir.RefConst{Value: "false", Type: "bool"}},
							Type:   "void",
						},
					},
					Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
				}},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	for _, call := range []string{"call void @accept(i1 true)", "call void @accept(i1 false)"} {
		if !strings.Contains(irText, call) {
			t.Fatalf("expected %q, got:\n%s", call, irText)
		}
	}
}

func TestGenerateLLVMIRRejectsNumericBooleanConstants(t *testing.T) {
	for _, value := range []string{"0", "1"} {
		t.Run(value, func(t *testing.T) {
			diag := diagnostics.NewDiagnosticBag()
			mod := &mir.Module{
				Name: "test",
				Funcs: []*mir.Function{{
					Name:       "invalid_bool",
					ReturnType: "bool",
					Blocks: []*mir.Block{{
						ID:   0,
						Term: &mir.Ret{Value: &mir.RefConst{Value: value, Type: "bool"}},
					}},
				}},
			}

			if irText := GenerateLLVMIR(mod, diag, "x86_64-unknown-linux-gnu", false, "linux"); irText != "" {
				t.Fatalf("numeric boolean constant must suppress LLVM output, got:\n%s", irText)
			}
			if !diag.HasErrors() {
				t.Fatal("numeric boolean constant must emit diagnostic")
			}
		})
	}
}

func TestGenerateLLVMIRLowersIntegerBitwiseOperators(t *testing.T) {
	tests := []struct {
		name        string
		op          string
		typeText    string
		instruction string
		shift       bool
	}{
		{name: "and", op: "&", typeText: "u8", instruction: " = and i8 %left, %right"},
		{name: "or", op: "|", typeText: "u8", instruction: " = or i8 %left, %right"},
		{name: "xor", op: "^", typeText: "u8", instruction: " = xor i8 %left, %right"},
		{name: "left shift", op: "<<", typeText: "u8", instruction: " = shl i8 %left, %right", shift: true},
		{name: "signed right shift", op: ">>", typeText: "i8", instruction: " = ashr i8 %left, %right", shift: true},
		{name: "unsigned right shift", op: ">>", typeText: "u8", instruction: " = lshr i8 %left, %right", shift: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &mir.RefName{Name: "result", Type: tt.typeText}
			mod := &mir.Module{
				Name: "test",
				Funcs: []*mir.Function{{
					Name:       "apply",
					Params:     []ir.Param{{Name: "left", Type: tt.typeText}, {Name: "right", Type: tt.typeText}},
					ReturnType: tt.typeText,
					Blocks: []*mir.Block{{
						ID: 0,
						Instrs: []mir.Instr{&mir.Assign{Name: "result", Value: &mir.Binary{
							Op: tt.op, Left: &mir.RefName{Name: "left", Type: tt.typeText}, Right: &mir.RefName{Name: "right", Type: tt.typeText}, Type: tt.typeText,
						}}},
						Term: &mir.Ret{Value: result},
					}},
				}},
			}
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
			if !strings.Contains(out, tt.instruction) {
				t.Fatalf("expected %s, got:\n%s", tt.instruction, out)
			}
			if tt.shift {
				guard := strings.Index(out, "icmp uge i8 %right, 8")
				trap := strings.Index(out, "call void @llvm.trap()")
				shift := strings.Index(out, tt.instruction)
				if guard < 0 || trap < guard || shift < trap {
					t.Fatalf("shift guard must dominate instruction, got:\n%s", out)
				}
			}
		})
	}
}

func TestGenerateLLVMIRLowersIntegerComplement(t *testing.T) {
	result := &mir.RefName{Name: "result", Type: "u8"}
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "complement",
			Params:     []ir.Param{{Name: "value", Type: "u8"}},
			ReturnType: "u8",
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "result", Value: &mir.Unary{
					Op: "~", Arg: &mir.RefName{Name: "value", Type: "u8"}, Type: "u8",
				}}},
				Term: &mir.Ret{Value: result},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if !strings.Contains(out, " = xor i8 %value, -1") {
		t.Fatalf("expected finite-width complement, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersOwnedPointerDrop(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "release",
			Params:     []ir.Param{{Name: "value", Type: "*i32"}},
			ReturnType: "void",
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: "*i32"}}},
				Term:   &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if !strings.Contains(out, "extractvalue { i32*, i8* } %value, 1") ||
		!strings.Contains(out, "extractvalue { i32*, i8* } %value, 0") ||
		!strings.Contains(out, "ptrtoint i32* getelementptr (i32, i32* null, i32 1) to i64") ||
		!strings.Contains(out, "@peeper_default_free_fn") {
		t.Fatalf("expected owned-pointer deallocation through allocator, got:\n%s", out)
	}
}

func TestGenerateLLVMIRReusesExistingFreeDeclaration(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{
			{Name: "free", Params: []ir.Param{{Name: "value", Type: "rawptr"}}, ReturnType: "void"},
			{
				Name:       "release",
				Params:     []ir.Param{{Name: "value", Type: "*i32"}},
				ReturnType: "void",
				Blocks: []*mir.Block{{
					ID:     0,
					Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: "*i32"}}},
					Term:   &mir.Ret{},
				}},
			},
		},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if count := strings.Count(out, "declare void @free(i8*)"); count != 1 {
		t.Fatalf("expected one free declaration, got %d:\n%s", count, out)
	}
	if !strings.Contains(out, "call void @free(i8*") {
		t.Fatalf("expected automatic destruction to reuse free declaration, got:\n%s", out)
	}
}

func TestGenerateLLVMIRRejectsIncompatibleFreeDeclaration(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{
			{Name: "free", Params: []ir.Param{{Name: "value", Type: "i32"}}, ReturnType: "void"},
			{
				Name:       "release",
				Params:     []ir.Param{{Name: "value", Type: "*i32"}},
				ReturnType: "void",
				Blocks: []*mir.Block{{
					ID:     0,
					Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: "*i32"}}},
					Term:   &mir.Ret{},
				}},
			},
		},
	}
	diag := diagnostics.NewDiagnosticBag()
	out := GenerateLLVMIR(mod, diag, "x86_64-unknown-linux-gnu", false, "linux")
	if out != "" {
		t.Fatalf("expected incompatible free ABI to suppress LLVM output, got:\n%s", out)
	}
	if !strings.Contains(diag.EmitAllToString(), "runtime symbol `free` must have signature fn(rawptr) -> void") {
		t.Fatalf("expected incompatible free ABI diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestGenerateLLVMIRLowersDynamicArrayAllocation(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "values",
			ReturnType: "[]i32",
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "values", Value: &mir.DynamicArrayAlloc{
					Length: 3,
					Type:   "[]i32",
				}}},
				Term: &mir.Ret{Value: &mir.RefName{Name: "values", Type: "[]i32"}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	for _, expected := range []string{
		"declare i8* @malloc(i64)",
		"@llvm.umul.with.overflow.i64",
		"ptrtoint i32* getelementptr",
		"select i1",
		"i64 1, i64",
		"call i8* @malloc(i64",
		"icmp eq i8*",
		"call void @llvm.trap()",
		"insertvalue { i32*, i64, i64 }",
		"i64 3, 2",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in dynamic allocation IR:\n%s", expected, out)
		}
	}
}

func TestGenerateLLVMIRLowersEmptyDynamicArrayWithoutAllocation(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "values",
			ReturnType: "[]i32",
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Assign{Name: "values", Value: &mir.DynamicArrayAlloc{Type: "[]i32"}}},
				Term:   &mir.Ret{Value: &mir.RefName{Name: "values", Type: "[]i32"}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if strings.Contains(out, "@malloc") || strings.Contains(out, "umul.with.overflow") {
		t.Fatalf("empty dynamic array must not allocate:\n%s", out)
	}
	if !strings.Contains(out, "ret { i32*, i64, i64 } zeroinitializer") {
		t.Fatalf("empty dynamic array must return zero header:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersDynamicArrayOwnerOperations(t *testing.T) {
	tests := []struct {
		name     string
		op       symbols.CompilerOp
		length   mir.ValueRef
		value    mir.ValueRef
		expected []string
	}{
		{
			name:  "append",
			op:    symbols.CompilerOpAppend,
			value: &mir.RefName{Name: "value", Type: "i32"},
			expected: []string{
				"array_append_capacity_", "@llvm.umul.with.overflow.i64", "array_relocate_loop_",
				"store i32 %value", "call void @free(i8*",
			},
		},
		{
			name:   "reserve",
			op:     symbols.CompilerOpReserve,
			length: &mir.RefName{Name: "size", Type: "usize"},
			expected: []string{
				"icmp uge i64", "array_reserve_reuse_", "array_relocate_loop_", "call i8* @malloc(i64", "call void @free(i8*",
			},
		},
		{
			name:   "resize",
			op:     symbols.CompilerOpResize,
			length: &mir.RefName{Name: "size", Type: "usize"},
			value:  &mir.RefName{Name: "value", Type: "i32"},
			expected: []string{
				"array_resize_loop_", "icmp ult i64", "store i32 %value", "insertvalue { i32*, i64, i64 }",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := dynamicArrayOperationModule(tt.name, "[]i32", tt.op, tt.length, tt.value)
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
			for _, expected := range append([]string{"declare i8* @malloc(i64)", "declare void @free(i8*)"}, tt.expected...) {
				if !strings.Contains(out, expected) {
					t.Fatalf("expected %q in %s IR:\n%s", expected, tt.name, out)
				}
			}
		})
	}
}

func TestGenerateLLVMIRLowersDynamicArrayShrink(t *testing.T) {
	tests := []struct {
		name      string
		arrayType string
		expected  []string
	}{
		{name: "scalar", arrayType: "[]i32", expected: []string{"array_shrink_drop_", "icmp ult i64", "array_shrink_done_"}},
		{name: "owner", arrayType: "[][]i32", expected: []string{"drop_array_loop_", "icmp ugt i64", "call void @free(i8*"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := dynamicArrayOperationModule(tt.name, tt.arrayType, symbols.CompilerOpShrink, &mir.RefName{Name: "size", Type: "usize"}, nil)
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
			if strings.Contains(out, "@malloc") || strings.Contains(out, "umul.with.overflow") {
				t.Fatalf("shrink must not allocate:\n%s", out)
			}
			for _, expected := range tt.expected {
				if !strings.Contains(out, expected) {
					t.Fatalf("expected %q in %s shrink IR:\n%s", expected, tt.name, out)
				}
			}
		})
	}
}

func TestGenerateLLVMIRLowersDynamicArrayOwnerOperationsFor32BitTarget(t *testing.T) {
	previousBits := target.SizeBits()
	if err := target.SetSizeBits(target.Bits32); err != nil {
		t.Fatalf("set 32-bit target: %v", err)
	}
	t.Cleanup(func() {
		if err := target.SetSizeBits(previousBits); err != nil {
			t.Fatalf("restore target size: %v", err)
		}
	})

	tests := []struct {
		name   string
		op     symbols.CompilerOp
		length mir.ValueRef
		value  mir.ValueRef
	}{
		{name: "append", op: symbols.CompilerOpAppend, value: &mir.RefName{Name: "value", Type: "i32"}},
		{name: "reserve", op: symbols.CompilerOpReserve, length: &mir.RefName{Name: "size", Type: "usize"}},
		{name: "resize", op: symbols.CompilerOpResize, length: &mir.RefName{Name: "size", Type: "usize"}, value: &mir.RefName{Name: "value", Type: "i32"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := dynamicArrayOperationModule(tt.name, "[]i32", tt.op, tt.length, tt.value)
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "i386-unknown-linux-gnu", false, "linux")
			for _, expected := range []string{"@llvm.umul.with.overflow.i32", "icmp ugt i64", "trunc i64"} {
				if !strings.Contains(out, expected) {
					t.Fatalf("expected %q in 32-bit %s IR:\n%s", expected, tt.name, out)
				}
			}
			if tt.length != nil && !strings.Contains(out, "zext i32 %size to i64") {
				t.Fatalf("expected usize normalization in 32-bit %s IR:\n%s", tt.name, out)
			}
		})
	}
}

func TestGenerateLLVMIRLowersDynamicArrayShrinkFor32BitTarget(t *testing.T) {
	previousBits := target.SizeBits()
	if err := target.SetSizeBits(target.Bits32); err != nil {
		t.Fatalf("set 32-bit target: %v", err)
	}
	t.Cleanup(func() {
		if err := target.SetSizeBits(previousBits); err != nil {
			t.Fatalf("restore target size: %v", err)
		}
	})
	mod := dynamicArrayOperationModule("shrink", "[]i32", symbols.CompilerOpShrink, &mir.RefName{Name: "size", Type: "usize"}, nil)
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "i386-unknown-linux-gnu", false, "linux")
	if !strings.Contains(out, "zext i32 %size to i64") {
		t.Fatalf("expected usize normalization in 32-bit shrink IR:\n%s", out)
	}
	if strings.Contains(out, "@malloc") || strings.Contains(out, "umul.with.overflow") {
		t.Fatalf("32-bit shrink must not allocate:\n%s", out)
	}
}

func dynamicArrayOperationModule(name, arrayType string, op symbols.CompilerOp, length, value mir.ValueRef) *mir.Module {
	params := []ir.Param{{Name: "values", Type: arrayType}}
	if length != nil {
		params = append(params, ir.Param{Name: "size", Type: "usize"})
	}
	if value != nil {
		params = append(params, ir.Param{Name: "value", Type: "i32"})
	}
	return &mir.Module{Name: "test", Funcs: []*mir.Function{{
		Name: name, Params: params, ReturnType: arrayType, Blocks: []*mir.Block{{
			ID: 0,
			Instrs: []mir.Instr{&mir.Assign{Name: "result", Value: &mir.DynamicArrayOp{
				Op: op, Array: &mir.RefName{Name: "values", Type: arrayType}, Length: length, Value: value, Type: arrayType,
			}}},
			Term: &mir.Ret{Value: &mir.RefName{Name: "result", Type: arrayType}},
		}},
	}}}
}

func TestGenerateLLVMIRReusesCompatibleMallocDeclaration(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{
			{Name: "malloc", Params: []ir.Param{{Name: "size", Type: "usize"}}, ReturnType: "rawptr"},
			{
				Name:       "values",
				ReturnType: "[]i32",
				Blocks: []*mir.Block{{
					ID:     0,
					Instrs: []mir.Instr{&mir.Assign{Name: "values", Value: &mir.DynamicArrayAlloc{Length: 1, Type: "[]i32"}}},
					Term:   &mir.Ret{Value: &mir.RefName{Name: "values", Type: "[]i32"}},
				}},
			},
		},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if count := strings.Count(out, "declare i8* @malloc(i64)"); count != 1 {
		t.Fatalf("expected one malloc declaration, got %d:\n%s", count, out)
	}
}

func TestGenerateLLVMIRRejectsIncompatibleMallocDeclaration(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{
			{Name: "malloc", Params: []ir.Param{{Name: "size", Type: "i32"}}, ReturnType: "rawptr"},
			{
				Name:       "values",
				ReturnType: "[]i32",
				Blocks: []*mir.Block{{
					ID:     0,
					Instrs: []mir.Instr{&mir.Assign{Name: "values", Value: &mir.DynamicArrayAlloc{Length: 1, Type: "[]i32"}}},
					Term:   &mir.Ret{Value: &mir.RefName{Name: "values", Type: "[]i32"}},
				}},
			},
		},
	}
	diag := diagnostics.NewDiagnosticBag()
	out := GenerateLLVMIR(mod, diag, "x86_64-unknown-linux-gnu", false, "linux")
	if out != "" {
		t.Fatalf("expected incompatible malloc ABI to suppress LLVM output, got:\n%s", out)
	}
	if !strings.Contains(diag.EmitAllToString(), "runtime symbol `malloc` must have signature fn(usize) -> rawptr") {
		t.Fatalf("expected incompatible malloc ABI diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestGenerateLLVMIRDropsNestedOwnersBeforeStorage(t *testing.T) {
	typeText := "*struct{child: *i32}"
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "release",
			Params:     []ir.Param{{Name: "value", Type: typeText}},
			ReturnType: "void",
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: typeText}}},
				Term:   &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if count := strings.Count(out, "ptrtoint i32* getelementptr (i32, i32* null, i32 1) to i64"); count != 1 {
		t.Fatalf("expected child size computation, got %d:\n%s", count, out)
	}
	if !strings.Contains(out, "@peeper_default_free_fn") {
		t.Fatalf("expected deallocation through descriptor, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersOwnedPointerStructLayout(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "pass",
			Params:     []ir.Param{{Name: "value", Type: "*i32"}},
			ReturnType: "*i32",
			Blocks: []*mir.Block{{
				ID:   0,
				Term: &mir.Ret{Value: &mir.RefName{Name: "value", Type: "*i32"}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if !strings.Contains(out, "define { i32*, i8* } @pass({ i32*, i8* } %value)") {
		t.Fatalf("expected owned pointer struct ABI {T*, i8*}, got:\n%s", out)
	}
	if !strings.Contains(out, "ret { i32*, i8* } %value") {
		t.Fatalf("expected owned pointer struct return, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersOptionalOwnedPointerAsTagged(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "nullable",
			Params:     []ir.Param{{Name: "opt", Type: "?*i32"}},
			ReturnType: "?*i32",
			Blocks: []*mir.Block{{
				ID:   0,
				Term: &mir.Ret{Value: &mir.RefName{Name: "opt", Type: "?*i32"}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if !strings.Contains(out, "define { i1, { i32*, i8* } } @nullable({ i1, { i32*, i8* } }") {
		t.Fatalf("expected tagged optional owned pointer ABI, got:\n%s", out)
	}
}

func TestGenerateLLVMIRDefaultDescriptorEmitted(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "drop",
			Params:     []ir.Param{{Name: "value", Type: "*i32"}},
			ReturnType: "void",
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: "*i32"}}},
				Term:   &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if !strings.Contains(out, "@peeper_default_alloc = private constant [3 x i8*]") {
		t.Fatalf("expected default descriptor global, got:\n%s", out)
	}
	if !strings.Contains(out, "@peeper_default_alloc_fn") || !strings.Contains(out, "@peeper_default_free_fn") {
		t.Fatalf("expected default descriptor thunks, got:\n%s", out)
	}
}

func TestGenerateLLVMIRNoDescriptorWithoutOwnedPointers(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "main",
			ReturnType: "i32",
			Blocks: []*mir.Block{{
				ID:   0,
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if strings.Contains(out, "@peeper_default_alloc") {
		t.Fatalf("unexpected default descriptor without owned pointers, got:\n%s", out)
	}
}

func TestGenerateLLVMIROwnedInterfaceAdoptsAllocationAndDropsPayload(t *testing.T) {
	const (
		payloadType   = "struct{child: *i32}"
		interfaceType = "*iface{}"
	)
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "release",
			Params:     []ir.Param{{Name: "resource", Type: "*" + payloadType}},
			ReturnType: "void",
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{
					&mir.Assign{Name: "erased", Value: &mir.InterfaceMake{
						Value:    &mir.RefName{Name: "resource", Type: "*" + payloadType},
						DataType: payloadType,
						Type:     interfaceType,
					}},
					&mir.Drop{Value: &mir.RefName{Name: "erased", Type: interfaceType}},
				},
				Term: &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if strings.Contains(out, "alloca "+payloadType) {
		t.Fatalf("owned interface conversion must adopt existing allocation, got:\n%s", out)
	}
	if !strings.Contains(out, "private constant [1 x i8*]") ||
		!strings.Contains(out, "define void @__iface_drop") ||
		!strings.Contains(out, "bitcast { { i32*, i8* } }* %") {
		t.Fatalf("expected direct fat carrier with payload-drop slot, got:\n%s", out)
	}
	if count := strings.Count(out, "@peeper_default_free_fn"); count < 2 {
		t.Fatalf("expected nested payload and carrier storage deallocs through descriptor, got %d:\n%s", count, out)
	}
}

func TestGenerateLLVMIRInterfaceMethodUsesSlotAfterDrop(t *testing.T) {
	const interfaceType = "&iface{read(self: &Self): i32}"
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{
			{
				Name:       "read_thunk",
				Params:     []ir.Param{{Name: "data", Type: "rawptr"}},
				ReturnType: "i32",
			},
			{
				Name:       "read",
				Params:     []ir.Param{{Name: "counter", Type: "&struct{value: i32}"}},
				ReturnType: "i32",
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "reader", Value: &mir.InterfaceMake{
							Value:    &mir.RefName{Name: "counter", Type: "&struct{value: i32}"},
							DataType: "struct{value: i32}",
							Slots:    []mir.ValueRef{&mir.RefName{Name: "read_thunk", Type: "fn(rawptr) -> i32"}},
							Type:     interfaceType,
						}},
						&mir.Assign{Name: "result", Value: &mir.InterfaceCall{
							Base: &mir.RefName{Name: "reader", Type: interfaceType},
							Slot: 0,
							Type: "i32",
						}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "result", Type: "i32"}},
				}},
			},
		},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if !strings.Contains(out, "private constant [2 x i8*]") ||
		!strings.Contains(out, "getelementptr inbounds i8*, i8**") ||
		!strings.Contains(out, "i32 1") {
		t.Fatalf("expected method dispatch after payload-drop slot, got:\n%s", out)
	}
}

func TestGenerateLLVMIRDropsDynamicArrayElementsInReverse(t *testing.T) {
	typeText := "[]*i32"
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "release",
			Params:     []ir.Param{{Name: "values", Type: typeText}},
			ReturnType: "void",
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "values", Type: typeText}}},
				Term:   &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	decrement := strings.Index(out, " = sub i64 ")
	elementLoad := strings.Index(out, " = getelementptr { i32*, i8* }, { i32*, i8* }* ")
	if !strings.Contains(out, " = icmp ugt i64 ") || decrement < 0 || elementLoad < decrement {
		t.Fatalf("expected reverse dynamic-array drop loop, got:\n%s", out)
	}
	if strings.Contains(out, " = phi i64 [ 0,") || strings.Contains(out, " = add i64 ") {
		t.Fatalf("dynamic-array drop must not advance from index zero, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersDynamicArraySliceViewAcrossTargets(t *testing.T) {
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{{
			Name:       "borrow",
			Params:     []ir.Param{{Name: "xs", Type: "[]i32"}},
			ReturnType: "i32",
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "view", Value: &mir.SliceView{
					Source: &mir.Place{Root: &mir.RefName{Name: "xs", Type: "[]i32"}, Type: "[]i32"},
					Type:   "&[]i32",
				}}},
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
			}},
		}},
	}
	targets := []struct {
		name     string
		triple   string
		targetOS string
	}{
		{name: "linux", triple: "x86_64-unknown-linux-gnu", targetOS: "linux"},
		{name: "darwin", triple: "aarch64-apple-darwin", targetOS: "darwin"},
		{name: "windows", triple: "x86_64-pc-windows-msvc", targetOS: "windows"},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), target.triple, false, target.targetOS)
			if !strings.Contains(irText, "extractvalue { i32*, i64, i64 } %xs, 0") ||
				!strings.Contains(irText, "extractvalue { i32*, i64, i64 } %xs, 1") {
				t.Fatalf("expected view to extract owner data and length, got:\n%s", irText)
			}
			if !strings.Contains(irText, "insertvalue { i32*, i64 } zeroinitializer, i32*") ||
				!strings.Contains(irText, "insertvalue { i32*, i64 }") ||
				!strings.Contains(irText, ", i64 %") {
				t.Fatalf("expected {ptr,len} slice view construction, got:\n%s", irText)
			}
		})
	}
}

func TestGenerateLLVMIRLowersCheckedInclusiveFixedArraySlice(t *testing.T) {
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{{
			Name:       "slice",
			Params:     []ir.Param{{Name: "xs", Type: "&mut [4]i32"}},
			ReturnType: "i32",
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "view", Value: &mir.SliceView{
					Source: &mir.Place{Root: &mir.RefName{Name: "xs", Type: "&mut [4]i32"}, Type: "&mut [4]i32"},
					Start:  &mir.RefConst{Value: "1", Type: "i32"},
					End:    &mir.RefConst{Value: "2", Type: "i32"},
					Type:   "&mut []i32",
				}}},
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
			}},
		}},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	compare := strings.Index(irText, "icmp uge i64")
	trap := strings.Index(irText, "call void @llvm.trap()")
	add := strings.Index(irText, "add i64")
	ready := strings.Index(irText, "\nslice_bounds_ready_")
	gep := strings.Index(irText, "getelementptr [4 x i32]")
	if compare < 0 || strings.Count(irText, "icmp ugt i64") < 2 || trap < compare || add < trap || ready < add || gep < ready {
		t.Fatalf("range checks must dominate inclusive conversion and fixed-array GEP, got:\n%s", irText)
	}
	if !strings.Contains(irText, "sub i64") ||
		!strings.Contains(irText, "insertvalue { i32*, i64 } zeroinitializer") {
		t.Fatalf("expected adjusted {data,len} slice view, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRReslicesSharedViewWithoutCapacity(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "slice",
			Params:     []ir.Param{{Name: "xs", Type: "&[]i32"}},
			ReturnType: "i32",
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "view", Value: &mir.SliceView{
					Source:       &mir.Place{Root: &mir.RefName{Name: "xs", Type: "&[]i32"}, Type: "&[]i32"},
					End:          &mir.RefConst{Value: "2", Type: "u8"},
					EndExclusive: true,
					Type:         "&[]i32",
				}}},
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
			}},
		}},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if !strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 0") ||
		!strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 1") {
		t.Fatalf("expected shared view data and length extraction, got:\n%s", irText)
	}
	if strings.Contains(irText, "{ i32*, i64, i64 }") {
		t.Fatalf("reslicing must not recover capacity, got:\n%s", irText)
	}
	trap := strings.Index(irText, "call void @llvm.trap()")
	ready := strings.Index(irText, "\nslice_bounds_ready_")
	gep := strings.Index(irText, "getelementptr i32")
	if strings.Count(irText, "icmp ugt i64") < 2 || trap < 0 || ready < trap || gep < ready {
		t.Fatalf("exclusive and reversed bounds checks must dominate adjusted GEP, got:\n%s", irText)
	}
}

func TestOptionalNicheLayout(t *testing.T) {
	if _, ok := optionalNicheLayout("*i32"); ok {
		t.Fatalf("optional owned pointer niche removed: {T*, i8*} has no null sentinel")
	}
	if _, ok := optionalNicheLayout("i32"); ok {
		t.Fatalf("plain integer must not use niche layout without invalid value rule")
	}
	if _, ok := optionalNicheLayout("*iface{}"); ok {
		t.Fatalf("fat owned interface must use tagged optional layout")
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
				ReturnType: "?*i32",
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "p", Value: &mir.ZeroValue{Type: "?*i32"}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "p", Type: "?*i32"}},
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
	if !strings.Contains(irText, "define { i1, { i32*, i8* } } @niche(") {
		t.Fatalf("expected tagged optional pointer return type, got:\n%s", irText)
	}
	if !strings.Contains(irText, "ret { i1, { i32*, i8* } } zeroinitializer") {
		t.Fatalf("expected tagged optional none as zeroinitializer for pointer, got:\n%s", irText)
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
				Params:     []ir.Param{{Name: "p", Type: "*i32"}},
				ReturnType: "?*i32",
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "x", Value: &mir.OptionalSome{Value: &mir.RefName{Name: "p", Type: "*i32"}, Type: "?*i32"}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "x", Type: "?*i32"}},
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
	if !strings.Contains(irText, "define { i1, { i32*, i8* } } @niche({ i32*, i8* } %p)") {
		t.Fatalf("expected tagged optional pointer ABI, got:\n%s", irText)
	}
	if !strings.Contains(irText, "insertvalue { i1, { i32*, i8* } } %") || !strings.Contains(irText, "{ i32*, i8* } %p, 1") {
		t.Fatalf("expected tagged optional pointer payload, got:\n%s", irText)
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

func TestGenerateLLVMIRLowersIndirectFieldPlaceWithoutTempAlloca(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	for _, baseType := range []string{"*struct{value: i32}", "&struct{value: i32}", "&mut struct{value: i32}"} {
		t.Run(baseType, func(t *testing.T) {
			mod := &mir.Module{
				Name:     "test",
				FilePath: unixTestPath,
				Funcs: []*mir.Function{
					{
						Name:       "main",
						Params:     []ir.Param{{Name: "box", Type: baseType}},
						ReturnType: "i32",
						EntryID:    0,
						Blocks: []*mir.Block{
							{
								ID: 0,
								Instrs: []mir.Instr{
									&mir.Assign{
										Name: "fieldptr",
										Value: &mir.AddrOf{
											Place: &mir.Place{
												Root: &mir.RefName{Name: "box", Type: baseType},
												Projections: []mir.PlaceProjection{
													{Kind: mir.PlaceProjectionDeref, Type: "struct{value: i32}"},
													{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: "i32"},
												},
												Type: "i32",
											},
											Type: "&mut i32",
										},
									},
								},
								Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
							},
						},
					},
				},
			}

			irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
			if _, isOwned := pointerTypeTextTarget(baseType); isOwned {
				if !strings.Contains(irText, "extractvalue { { i32 }*, i8* }") {
					t.Fatalf("expected extractvalue for owned pointer struct, got:\n%s", irText)
				}
			}
			if !strings.Contains(irText, "getelementptr inbounds { i32 }, { i32 }*") {
				t.Fatalf("expected field address to lower as GEP, got:\n%s", irText)
			}
			if strings.Contains(irText, "alloca i32") {
				t.Fatalf("unexpected temp alloca for field address, got:\n%s", irText)
			}
			if strings.Contains(irText, "load i32") {
				t.Fatalf("unexpected field load for field address, got:\n%s", irText)
			}
		})
	}
}

func TestGenerateLLVMIRCastsProjectedFieldAddressToRawptr(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{{
			Name:       "main",
			Params:     []ir.Param{{Name: "box", Type: "&mut struct{value: i32}"}},
			ReturnType: "i32",
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{
					&mir.Assign{
						Name: "fieldptr",
						Value: &mir.AddrOf{
							Place: &mir.Place{
								Root: &mir.RefName{Name: "box", Type: "&mut struct{value: i32}"},
								Projections: []mir.PlaceProjection{
									{Kind: mir.PlaceProjectionDeref, Type: "struct{value: i32}"},
									{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: "i32"},
								},
								Type: "i32",
							},
							Type: "&mut i32",
						},
					},
					&mir.Assign{
						Name:  "raw",
						Value: &mir.Cast{Arg: &mir.RefName{Name: "fieldptr", Type: "&mut i32"}, Type: "rawptr"},
					},
				},
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
			}},
		}},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "getelementptr inbounds { i32 }, { i32 }* %box") ||
		!strings.Contains(irText, "bitcast i32*") || !strings.Contains(irText, "to i8*") {
		t.Fatalf("expected projected field address rawptr cast, got:\n%s", irText)
	}
}

func indexedMIRPlace(baseType string, index mir.ValueRef) *mir.Place {
	return &mir.Place{
		Root: &mir.RefName{Name: "xs", Type: baseType},
		Projections: []mir.PlaceProjection{
			{Kind: mir.PlaceProjectionIndex, Index: index, Type: "i32"},
		},
		Type: "i32",
	}
}

func indexReadMIRModule(baseType string, index mir.ValueRef) *mir.Module {
	params := []ir.Param{{Name: "xs", Type: baseType}}
	if ref, ok := index.(*mir.RefName); ok {
		params = append(params, ir.Param{Name: ref.Name, Type: ref.Type})
	}
	return &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "first",
				Params:     params,
				ReturnType: "i32",
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{
								Name:  "item",
								Value: &mir.Load{Place: indexedMIRPlace(baseType, index), Type: "i32"},
							},
						},
						Term: &mir.Ret{Value: &mir.RefName{Name: "item", Type: "i32"}},
					},
				},
			},
		},
	}
}

func indexStoreMIRModule(baseType string, index mir.ValueRef) *mir.Module {
	return &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name: "set_item",
				Params: []ir.Param{
					{Name: "xs", Type: baseType},
					{Name: "value", Type: "i32"},
				},
				ReturnType: "i32",
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Store{
								Place: indexedMIRPlace(baseType, index),
								Value: &mir.RefName{Name: "value", Type: "i32"},
							},
						},
						Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: "i32"}},
					},
				},
			},
		},
	}
}

func TestGenerateLLVMIRLowersIndexPlaceForArrayRead(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := indexReadMIRModule("[4]i32", &mir.RefConst{Value: "0", Type: "i32"})
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "getelementptr inbounds [4 x i32], [4 x i32]*") {
		t.Fatalf("expected array index to lower as GEP, got:\n%s", irText)
	}
	if !strings.Contains(irText, "load i32, i32*") {
		t.Fatalf("expected array index read to load element, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRBoundsChecksRuntimeFixedArrayIndex(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := indexReadMIRModule("[4]i32", &mir.RefName{Name: "index", Type: "i32"})
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	for _, expected := range []string{"sext i32 %index to i64", "icmp uge i64", "call void @llvm.trap()", "getelementptr inbounds [4 x i32]"} {
		if !strings.Contains(irText, expected) {
			t.Fatalf("expected %q in runtime fixed-array index IR:\n%s", expected, irText)
		}
	}
}

func TestGenerateLLVMIRBoundsChecksWideRuntimeFixedArrayIndexBeforeTruncation(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := indexReadMIRModule("[4]i32", &mir.RefName{Name: "index", Type: "u128"})
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	for _, expected := range []string{"zext i64 4 to i128", "icmp uge i128 %index", "trunc i128 %index to i64", "getelementptr inbounds [4 x i32]"} {
		if !strings.Contains(irText, expected) {
			t.Fatalf("expected %q in wide fixed-array index IR:\n%s", expected, irText)
		}
	}
}

func TestGenerateLLVMIRLowersIndexPlaceStoreForArrayWrite(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := indexStoreMIRModule("[4]i32", &mir.RefConst{Value: "0", Type: "i32"})
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "getelementptr inbounds [4 x i32], [4 x i32]*") {
		t.Fatalf("expected array index write to lower as GEP, got:\n%s", irText)
	}
	if !strings.Contains(irText, "store i32 %value, i32*") {
		t.Fatalf("expected array index write to store element, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLowersArrayLiteral(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "make",
				ReturnType: "[3]i32",
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{
								Name: "arr",
								Value: &mir.ArrayLit{
									Values: []mir.ValueRef{
										&mir.RefConst{Value: "1", Type: "i32"},
										&mir.RefConst{Value: "2", Type: "i32"},
										&mir.RefConst{Value: "3", Type: "i32"},
									},
									Type: "[3]i32",
								},
							},
						},
						Term: &mir.Ret{Value: &mir.RefName{Name: "arr", Type: "[3]i32"}},
					},
				},
			},
		},
	}
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "insertvalue [3 x i32] zeroinitializer, i32 1, 0") {
		t.Fatalf("expected array literal insertvalue, got:\n%s", irText)
	}
	if !strings.Contains(irText, "insertvalue [3 x i32] %") || !strings.Contains(irText, "i32 3, 2") {
		t.Fatalf("expected array literal final insertvalue, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRRejectsConstantArrayIndexOutOfBounds(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	diag := diagnostics.NewDiagnosticBag()
	irText := GenerateLLVMIR(indexReadMIRModule("[4]i32", &mir.RefConst{Value: "4", Type: "i32"}), diag, targetTriple, false, "linux")
	if irText != "" {
		t.Fatalf("expected out-of-bounds index to suppress LLVM output, got:\n%s", irText)
	}
	if !strings.Contains(diag.EmitAllToString(), "array index out of bounds: index 4 for length 4") {
		t.Fatalf("expected out-of-bounds diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestGenerateLLVMIRRejectsInvalidConstantArrayIndexes(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	for _, index := range []string{"-1", "bad"} {
		diag := diagnostics.NewDiagnosticBag()
		irText := GenerateLLVMIR(indexReadMIRModule("[4]i32", &mir.RefConst{Value: index, Type: "i32"}), diag, targetTriple, false, "linux")
		if irText != "" {
			t.Fatalf("expected invalid index %q to suppress LLVM output, got:\n%s", index, irText)
		}
		want := "array index out of bounds: index " + index + " for length 4"
		if !strings.Contains(diag.EmitAllToString(), want) {
			t.Fatalf("expected %q diagnostic, got:\n%s", want, diag.EmitAllToString())
		}
	}
}

func TestGenerateLLVMIRLowersDynamicArrayIndexRead(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	irText := GenerateLLVMIR(indexReadMIRModule("[]i32", &mir.RefName{Name: "i", Type: "i32"}), diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "extractvalue { i32*, i64, i64 } %xs, 0") {
		t.Fatalf("expected dynamic array index to extract data pointer, got:\n%s", irText)
	}
	if !strings.Contains(irText, "extractvalue { i32*, i64, i64 } %xs, 1") ||
		!strings.Contains(irText, "sext i32 %i to i64") ||
		!strings.Contains(irText, "icmp uge i64") ||
		!strings.Contains(irText, "call void @llvm.trap()") {
		t.Fatalf("expected dynamic array bounds guard, got:\n%s", irText)
	}
	if !strings.Contains(irText, "getelementptr i32, i32*") || !strings.Contains(irText, ", i64 %") {
		t.Fatalf("expected dynamic array index to lower as element GEP, got:\n%s", irText)
	}
	if !strings.Contains(irText, "load i32, i32*") {
		t.Fatalf("expected dynamic array index read to load element, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRUsesWidenedUnsignedDynamicArrayIndexForGEP(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	irText := GenerateLLVMIR(indexReadMIRModule("[]i32", &mir.RefName{Name: "i", Type: "u8"}), diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "zext i8 %i to i64") {
		t.Fatalf("expected narrow unsigned index widening, got:\n%s", irText)
	}
	gep := ""
	for line := range strings.SplitSeq(irText, "\n") {
		if strings.Contains(line, "getelementptr i32") {
			gep = line
			break
		}
	}
	if gep == "" || !strings.Contains(gep, "i64 %") || strings.Contains(gep, "i8 %i") {
		t.Fatalf("expected GEP to use widened unsigned index, got %q:\n%s", gep, irText)
	}
}

func TestGenerateLLVMIRLowersDynamicArrayIndexStore(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	irText := GenerateLLVMIR(indexStoreMIRModule("[]i32", &mir.RefConst{Value: "0", Type: "i32"}), diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "extractvalue { i32*, i64, i64 } %xs, 0") {
		t.Fatalf("expected dynamic array index to extract data pointer, got:\n%s", irText)
	}
	if !strings.Contains(irText, "icmp uge i64") || !strings.Contains(irText, "call void @llvm.trap()") {
		t.Fatalf("expected dynamic array store bounds guard, got:\n%s", irText)
	}
	if !strings.Contains(irText, "getelementptr i32, i32*") {
		t.Fatalf("expected dynamic array index to lower as element GEP, got:\n%s", irText)
	}
	if !strings.Contains(irText, "store i32 %value, i32*") {
		t.Fatalf("expected dynamic array index store to write element, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLowersSharedSliceViewIndexRead(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	irText := GenerateLLVMIR(indexReadMIRModule("&[]i32", &mir.RefName{Name: "i", Type: "i32"}), diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 0") ||
		!strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 1") {
		t.Fatalf("expected slice-view data and length extraction, got:\n%s", irText)
	}
	if strings.Contains(irText, "extractvalue { i32*, i64, i64 } %xs") {
		t.Fatalf("slice view must not use dynamic-array capacity layout, got:\n%s", irText)
	}
	if !strings.Contains(irText, "sext i32 %i to i64") ||
		!strings.Contains(irText, "icmp uge i64") ||
		!strings.Contains(irText, "call void @llvm.trap()") {
		t.Fatalf("expected slice-view bounds guard, got:\n%s", irText)
	}
	if !strings.Contains(irText, "getelementptr i32, i32*") || !strings.Contains(irText, "load i32, i32*") {
		t.Fatalf("expected slice-view place load, got:\n%s", irText)
	}
	compare := strings.Index(irText, "icmp uge i64")
	trap := strings.Index(irText, "call void @llvm.trap()")
	okBlock := strings.Index(irText, "\nbounds_ok_")
	gep := strings.Index(irText, "getelementptr i32")
	if compare < 0 || trap < compare || okBlock < trap || gep < okBlock {
		t.Fatalf("bounds guard must dominate slice-view GEP, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLowersMutableSliceViewIndexStore(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	irText := GenerateLLVMIR(indexStoreMIRModule("&mut []i32", &mir.RefConst{Value: "0", Type: "u8"}), diagnostics.NewDiagnosticBag(), targetTriple, false, "linux")
	if !strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 0") ||
		!strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 1") {
		t.Fatalf("expected mutable slice-view data and length extraction, got:\n%s", irText)
	}
	if !strings.Contains(irText, "zext i8 0 to i64") ||
		!strings.Contains(irText, "icmp uge i64") ||
		!strings.Contains(irText, "call void @llvm.trap()") {
		t.Fatalf("expected mutable slice-view bounds guard, got:\n%s", irText)
	}
	if !strings.Contains(irText, "getelementptr i32, i32*") || !strings.Contains(irText, "store i32 %value, i32*") {
		t.Fatalf("expected mutable slice-view place store, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLowersDeepMixedPlace(t *testing.T) {
	const tokenType = "struct{value: i32}"
	const bucketType = "struct{items: []" + tokenType + "}"
	place := &mir.Place{
		Root: &mir.RefName{Name: "bucket", Type: "&mut " + bucketType},
		Projections: []mir.PlaceProjection{
			{Kind: mir.PlaceProjectionDeref, Type: bucketType},
			{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: "[]" + tokenType},
			{Kind: mir.PlaceProjectionIndex, Index: &mir.RefName{Name: "index", Type: "usize"}, Type: tokenType},
			{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: "i32"},
		},
		Type: "i32",
	}
	mod := &mir.Module{Name: "test", Funcs: []*mir.Function{{
		Name: "read", Params: []ir.Param{{Name: "bucket", Type: "&mut " + bucketType}, {Name: "index", Type: "usize"}},
		ReturnType: "i32", EntryID: 0,
		Blocks: []*mir.Block{{
			ID:     0,
			Instrs: []mir.Instr{&mir.Assign{Name: "value", Value: &mir.Load{Place: place, Type: "i32"}}},
			Term:   &mir.Ret{Value: &mir.RefName{Name: "value", Type: "i32"}},
		}},
	}}}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	headerField := strings.Index(irText, "getelementptr inbounds { { { i32 }*, i64, i64 } }")
	element := strings.Index(irText, "getelementptr { i32 }, { i32 }*")
	valueField := strings.LastIndex(irText, "getelementptr inbounds { i32 }, { i32 }*")
	if headerField < 0 || element < headerField || valueField < element || !strings.Contains(irText, "load i32, i32*") {
		t.Fatalf("deep place projections must lower in path order, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRAllocatesPlaceRootBeforeBranches(t *testing.T) {
	const boxType = "struct{value: i32}"
	place := &mir.Place{
		Root: &mir.RefName{Name: "box", Type: boxType},
		Projections: []mir.PlaceProjection{
			{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: "i32"},
		},
		Type: "i32",
	}
	mod := &mir.Module{Name: "test", Funcs: []*mir.Function{{
		Name: "choose", Params: []ir.Param{{Name: "cond", Type: "bool"}}, ReturnType: "i32", EntryID: 0,
		Blocks: []*mir.Block{
			{ID: 0, Instrs: []mir.Instr{&mir.Assign{Name: "box", Value: &mir.StructLit{Fields: []mir.ValueRef{&mir.RefConst{Value: "0", Type: "i32"}}, Type: boxType}}}, Term: &mir.Branch{Cond: &mir.RefName{Name: "cond", Type: "bool"}, ThenID: 1, ElseID: 2}},
			{ID: 1, Instrs: []mir.Instr{&mir.Store{Place: place, Value: &mir.RefConst{Value: "1", Type: "i32"}}}, Term: &mir.Jump{TargetID: 3}},
			{ID: 2, Instrs: []mir.Instr{&mir.Store{Place: place, Value: &mir.RefConst{Value: "2", Type: "i32"}}}, Term: &mir.Jump{TargetID: 3}},
			{ID: 3, Instrs: []mir.Instr{&mir.Assign{Name: "value", Value: &mir.Load{Place: place, Type: "i32"}}}, Term: &mir.Ret{Value: &mir.RefName{Name: "value", Type: "i32"}}},
		},
	}}}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	alloca := strings.Index(irText, "alloca { i32 }")
	firstBranch := strings.Index(irText, "\nb1:")
	if alloca < 0 || firstBranch < 0 || alloca > firstBranch || strings.Count(irText, "alloca { i32 }") != 1 {
		t.Fatalf("place root must have one entry-dominating slot, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRConsumingInterfaceCallReleasesStorage(t *testing.T) {
	const interfaceType = "*iface{take(self: Self)}"
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "consume",
			Params:     []ir.Param{{Name: "value", Type: interfaceType}},
			ReturnType: "void",
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.InterfaceCall{
					Base:     &mir.RefName{Name: "value", Type: interfaceType},
					Slot:     0,
					Consumes: true,
					Type:     "void",
				}},
				Term: &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	callIndex := strings.Index(out, "call void %")
	freeIndex := strings.Index(out, "call void @free(i8*")
	if !strings.Contains(out, "declare void @free(i8*)") || callIndex < 0 || freeIndex < callIndex {
		t.Fatalf("expected consuming dispatch before carrier storage free, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersAlloc(t *testing.T) {
	mod := &mir.Module{
		Name: "test",
		Funcs: []*mir.Function{{
			Name:       "main",
			ReturnType: "*i32",
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{
					&mir.Assign{
						Name:  "p",
						Value: &mir.Alloc{Value: &mir.RefConst{Value: "42", Type: "i32"}, Type: "*i32"},
					},
				},
				Term: &mir.Ret{Value: &mir.RefName{Name: "p", Type: "*i32"}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), "x86_64-unknown-linux-gnu", false, "linux")
	if !strings.Contains(out, "@peeper_default_alloc") {
		t.Fatalf("expected default allocator descriptor, got:\n%s", out)
	}
	if !strings.Contains(out, "bitcast [3 x i8*]* @peeper_default_alloc to i8**") {
		t.Fatalf("expected descriptor bitcast, got:\n%s", out)
	}
	if !strings.Contains(out, "getelementptr i8*, i8** %") {
		t.Fatalf("expected descriptor index, got:\n%s", out)
	}
	if !strings.Contains(out, "ptrtoint i32* getelementptr (i32, i32* null, i32 1) to i64") {
		t.Fatalf("expected size computation, got:\n%s", out)
	}
	if !strings.Contains(out, "insertvalue { i32*, i8* }") {
		t.Fatalf("expected owned pointer carrier construction, got:\n%s", out)
	}
	if !strings.Contains(out, "icmp eq i8*") {
		t.Fatalf("expected null check for allocation, got:\n%s", out)
	}
}
