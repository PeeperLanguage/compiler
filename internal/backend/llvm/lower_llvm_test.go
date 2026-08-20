package llvm

import (
	"os/exec"
	"path/filepath"
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

var (
	testLinuxAMD64   = mustTestTarget("linux", "amd64")
	testLinux386     = mustTestTarget("linux", "386")
	testDarwinARM64  = mustTestTarget("darwin", "arm64")
	testWindowsAMD64 = mustTestTarget("windows", "amd64")
)

type llvmTypeFixture struct {
	table                                                        *ir.TypeTable
	void, boolType, cstr, stringType, rawptr, i32                ir.TypeID
	i8, u8, u128, usize, ownedI32, optionalI32, optionalOwnedI32 ir.TypeID
	dynamicI32, dynamicDynamicI32, fixed3I32, fixed4I32          ir.TypeID
	refI32, mutRefI32, refDynamicI32, mutRefDynamicI32           ir.TypeID
	refSliceI32, mutRefSliceI32, mutRefFixed4I32                 ir.TypeID
	valueStruct, refValueStruct                                  ir.TypeID
	ownedValueStruct, fnI32, fnVoid, fnBoolVoid                  ir.TypeID
	fnRawptrI32                                                  ir.TypeID
}

var llvmTypes = newLLVMTypeFixture(target.Bits64)

func newLLVMTypeFixture(indexBits int) llvmTypeFixture {
	table := ir.NewTypeTable()
	void := table.Intern(ir.Type{Kind: ir.TypeVoid})
	i32 := table.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
	usize := table.Intern(ir.Type{Kind: ir.TypeInteger, Bits: indexBits})
	table.SetIndexType(usize)
	boolType := table.Intern(ir.Type{Kind: ir.TypeBool})
	rawptr := table.Intern(ir.Type{Kind: ir.TypeRawPtr})
	dynamicI32 := table.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32})
	sliceI32 := table.Intern(ir.Type{Kind: ir.TypeSlice, Elem: i32})
	valueStruct := table.Intern(ir.Type{Kind: ir.TypeStruct, Fields: []ir.TypeField{{Name: "value", Type: i32}}})
	return llvmTypeFixture{
		table:             table,
		void:              void,
		boolType:          boolType,
		cstr:              table.Intern(ir.Type{Kind: ir.TypeCStr}),
		stringType:        table.Intern(ir.Type{Kind: ir.TypeString}),
		rawptr:            rawptr,
		i32:               i32,
		i8:                table.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 8}),
		u8:                table.Intern(ir.Type{Kind: ir.TypeInteger, Bits: 8}),
		u128:              table.Intern(ir.Type{Kind: ir.TypeInteger, Bits: 128}),
		usize:             usize,
		ownedI32:          table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: i32}),
		optionalI32:       table.Intern(ir.Type{Kind: ir.TypeOptional, Elem: i32}),
		optionalOwnedI32:  table.Intern(ir.Type{Kind: ir.TypeOptional, Elem: table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: i32})}),
		dynamicI32:        dynamicI32,
		dynamicDynamicI32: table.Intern(ir.Type{Kind: ir.TypeArray, Elem: dynamicI32}),
		fixed3I32:         table.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32, Length: "3"}),
		fixed4I32:         table.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32, Length: "4"}),
		refI32:            table.Intern(ir.Type{Kind: ir.TypeReference, Elem: i32}),
		mutRefI32:         table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: i32}),
		refDynamicI32:     table.Intern(ir.Type{Kind: ir.TypeReference, Elem: dynamicI32}),
		mutRefDynamicI32:  table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: dynamicI32}),
		refSliceI32:       table.Intern(ir.Type{Kind: ir.TypeReference, Elem: sliceI32}),
		mutRefSliceI32:    table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: sliceI32}),
		mutRefFixed4I32:   table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: table.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32, Length: "4"})}),
		valueStruct:       valueStruct,
		refValueStruct:    table.Intern(ir.Type{Kind: ir.TypeReference, Elem: valueStruct}),
		ownedValueStruct:  table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: valueStruct}),
		fnI32:             table.Intern(ir.Type{Kind: ir.TypeFunction, Return: i32}),
		fnVoid:            table.Intern(ir.Type{Kind: ir.TypeFunction, Return: void}),
		fnBoolVoid:        table.Intern(ir.Type{Kind: ir.TypeFunction, Params: []ir.TypeID{boolType}, Return: void}),
		fnRawptrI32:       table.Intern(ir.Type{Kind: ir.TypeFunction, Params: []ir.TypeID{rawptr}, Return: i32}),
	}
}

func mustTestTarget(os, arch string) target.Info {
	info, err := target.New(os, arch)
	if err != nil {
		panic(err)
	}
	return info
}

func TestLLVMLayoutModelTypes(t *testing.T) {
	types := llvmTypes.table
	byteType := types.Intern(ir.Type{Kind: ir.TypeByte})
	interfaceType := types.Intern(ir.Type{Kind: ir.TypeInterface})
	ownedInterface := types.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: interfaceType})
	cases := []struct {
		id   ir.TypeID
		want string
	}{
		{byteType, "i8"},
		{types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 24}), "i24"},
		{types.Intern(ir.Type{Kind: ir.TypeInteger, Bits: 8388608}), "i8388608"},
		{llvmTypes.stringType, "{ i8*, i64, i8* }"},
		{llvmTypes.optionalI32, "{ i1, i32 }"},
		{types.Intern(ir.Type{Kind: ir.TypeOptional, Elem: llvmTypes.stringType}), "{ i1, { i8*, i64, i8* } }"},
		{llvmTypes.optionalOwnedI32, "{ i1, { i32*, i8* } }"},
		{types.Intern(ir.Type{Kind: ir.TypeOptional, Elem: ownedInterface}), "{ i1, { i8*, i8*, i8* } }"},
		{llvmTypes.ownedI32, "{ i32*, i8* }"},
		{ownedInterface, "{ i8*, i8*, i8* }"},
		{llvmTypes.rawptr, "i8*"},
		{llvmTypes.fixed4I32, "[4 x i32]"},
		{llvmTypes.dynamicI32, "{ i32*, i64, i64, i8* }"},
		{llvmTypes.refI32, "i32*"},
		{llvmTypes.mutRefI32, "i32*"},
		{llvmTypes.refDynamicI32, "{ i32*, i64, i64, i8* }*"},
		{llvmTypes.mutRefDynamicI32, "{ i32*, i64, i64, i8* }*"},
		{llvmTypes.refSliceI32, "{ i32*, i64 }"},
		{llvmTypes.mutRefSliceI32, "{ i32*, i64 }"},
		{types.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: llvmTypes.stringType}), "{ { i8*, i64, i8* }*, i8* }"},
		{types.Intern(ir.Type{Kind: ir.TypeArray, Elem: types.Intern(ir.Type{Kind: ir.TypeOptional, Elem: llvmTypes.stringType})}), "{ { i1, { i8*, i64, i8* } }*, i64, i64, i8* }"},
		{types.Intern(ir.Type{Kind: ir.TypeStruct, Fields: []ir.TypeField{{Name: "x", Type: types.Intern(ir.Type{Kind: ir.TypeArray, Elem: llvmTypes.u8, Length: "2"})}}}), "{ [2 x i8] }"},
	}
	for _, tt := range cases {
		got, ok := llvmLayoutID(types, tt.id)
		if !ok || got.Text != tt.want {
			t.Fatalf("llvmLayoutID(%s) = %v, %v; want %q, true", types.Text(tt.id), got, ok, tt.want)
		}
	}
}

func TestLLVMLayoutUsesContextSizedUsize(t *testing.T) {
	for _, tt := range []struct {
		name string
		info target.Info
		want string
	}{
		{name: "32-bit", info: testLinux386, want: "i32"},
		{name: "64-bit", info: testLinuxAMD64, want: "i64"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			types := newLLVMTypeFixture(tt.info.IndexBits)
			got, ok := llvmLayoutID(types.table, types.usize)
			if !ok || got.Text != tt.want {
				t.Fatalf("llvmLayoutID(usize) = %v, %v; want %q, true", got, ok, tt.want)
			}
			refString := types.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: types.stringType})
			got, ok = llvmLayoutID(types.table, refString)
			wantRefString := "{ i8*, " + tt.want + " }"
			if !ok || got.Text != wantRefString {
				t.Fatalf("llvmLayoutID(&str) = %v, %v; want %q, true", got, ok, wantRefString)
			}
		})
	}
}

func requireLLVMInvariant(t *testing.T, emit func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered == nil || !strings.Contains(recovered.(string), "llvm invariant:") {
			t.Fatalf("expected LLVM invariant panic, got %#v", recovered)
		}
	}()
	emit()
}

func TestLLVMLayoutsNameBuiltInCarrierFields(t *testing.T) {
	interfaceType := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeInterface})
	ownedInterface := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: interfaceType})
	borrowedString := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: llvmTypes.stringType})
	for _, tt := range []struct {
		name   string
		typeID ir.TypeID
		fields map[llvmFieldName]int
	}{
		{name: "owned string", typeID: llvmTypes.stringType, fields: map[llvmFieldName]int{llvmFieldData: 0, llvmFieldLength: 1, llvmFieldAllocator: 2}},
		{name: "borrowed string", typeID: borrowedString, fields: map[llvmFieldName]int{llvmFieldData: 0, llvmFieldLength: 1}},
		{name: "borrowed array", typeID: llvmTypes.refSliceI32, fields: map[llvmFieldName]int{llvmFieldData: 0, llvmFieldLength: 1}},
		{name: "dynamic array", typeID: llvmTypes.dynamicI32, fields: map[llvmFieldName]int{llvmFieldData: 0, llvmFieldLength: 1, llvmFieldCapacity: 2, llvmFieldAllocator: 3}},
		{name: "optional", typeID: llvmTypes.optionalI32, fields: map[llvmFieldName]int{llvmFieldPresent: 0, llvmFieldValue: 1}},
		{name: "owned pointer", typeID: llvmTypes.ownedI32, fields: map[llvmFieldName]int{llvmFieldData: 0, llvmFieldAllocator: 1}},
		{name: "owned interface", typeID: ownedInterface, fields: map[llvmFieldName]int{llvmFieldData: 0, llvmFieldDispatch: 1, llvmFieldAllocator: 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			layout, ok := llvmLayoutID(llvmTypes.table, tt.typeID)
			if !ok {
				t.Fatalf("layout missing for %s", tt.name)
			}
			for field, index := range tt.fields {
				if layout.Fields[field] != index {
					t.Fatalf("%s field %q = %d, want %d", tt.name, field, layout.Fields[field], index)
				}
			}
		})
	}
}

func TestTypedLLVMBuilderRejectsOperandMismatches(t *testing.T) {
	i32 := llvmScalarLayout("i32")
	i64 := llvmScalarLayout("i64")
	aggregate := llvmAggregateLayout([]*llvmLayout{i32}, nil)
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	for name, emit := range map[string]func(*llvmBuilder){
		"comparison width": func(b *llvmBuilder) {
			b.compare("icmp", "eq", b.value("1", i32), b.value("1", i64))
		},
		"aggregate bitcast": func(b *llvmBuilder) {
			b.bitcast(b.value("zeroinitializer", aggregate), rawPointer)
		},
		"call argument width": func(b *llvmBuilder) {
			callee := b.value("@take", llvmFunctionLayout(&llvmLayout{Text: "void", Kind: llvmLayoutVoid}, []*llvmLayout{i32}))
			b.call(callee, []llvmValue{b.value("1", i64)})
		},
		"store pointee": func(b *llvmBuilder) {
			b.store(b.place("%ptr", i32), b.value("1", i64))
		},
		"return width": func(b *llvmBuilder) {
			b.ret(b.value("1", i64), i32)
		},
	} {
		t.Run(name, func(t *testing.T) {
			requireLLVMInvariant(t, func() {
				b := newLLVMBuilder(&strings.Builder{}, nil, -1)
				emit(b)
			})
		})
	}
}

func TestTypedLLVMBuilderPreservesPointerPlaceOnValidBitcast(t *testing.T) {
	var out strings.Builder
	b := newLLVMBuilder(&out, nil, -1)
	i32 := llvmScalarLayout("i32")
	rawPointer := llvmPointerLayout(llvmScalarLayout("i8"))
	place := b.alloca(i32)
	b.store(place, b.value("7", i32))
	loaded := b.load(place)
	if loaded.Layout.Text != "i32" {
		t.Fatalf("loaded layout = %s", loaded.Layout.Text)
	}
	cast := b.bitcast(b.pointerValue(place), rawPointer)
	if cast.Layout.Text != "i8*" || !strings.Contains(out.String(), "bitcast i32*") {
		t.Fatalf("pointer cast = %#v\n%s", cast, out.String())
	}
}

func TestLLVMFloatConstantsUseWidthCorrectHex(t *testing.T) {
	f32 := llvmFloatConst("2.4", 32)
	f64 := llvmFloatConst("2.4", 64)
	if !strings.HasPrefix(f32, "0x") || !strings.HasPrefix(f64, "0x") || f32 == f64 {
		t.Fatalf("float constants: f32=%q f64=%q", f32, f64)
	}
}

func TestGenerateLLVMIRLowersBooleanCallArguments(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{
			{Name: "accept", Params: []ir.Param{{Name: "value", Type: llvmTypes.boolType}}, ReturnType: llvmTypes.void},
			{
				Name:       "main",
				ReturnType: llvmTypes.i32,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Call{
							Callee: &mir.RefName{Name: "accept", Type: llvmTypes.fnBoolVoid},
							Args:   []mir.ValueRef{&mir.RefConst{Value: "true", Type: llvmTypes.boolType}},
							Type:   llvmTypes.void,
						},
						&mir.Call{
							Callee: &mir.RefName{Name: "accept", Type: llvmTypes.fnBoolVoid},
							Args:   []mir.ValueRef{&mir.RefConst{Value: "false", Type: llvmTypes.boolType}},
							Type:   llvmTypes.void,
						},
					},
					Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
				}},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
				Name: "test", Types: llvmTypes.table,
				Funcs: []*mir.Function{{
					Name:       "invalid_bool",
					ReturnType: llvmTypes.boolType,
					Blocks: []*mir.Block{{
						ID:   0,
						Term: &mir.Ret{Value: &mir.RefConst{Value: value, Type: llvmTypes.boolType}},
					}},
				}},
			}

			if irText := GenerateLLVMIR(mod, diag, testLinuxAMD64, false); irText != "" {
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
		typeID      ir.TypeID
		instruction string
		shift       bool
	}{
		{name: "and", op: "&", typeID: llvmTypes.u8, instruction: " = and i8 %left, %right"},
		{name: "or", op: "|", typeID: llvmTypes.u8, instruction: " = or i8 %left, %right"},
		{name: "xor", op: "^", typeID: llvmTypes.u8, instruction: " = xor i8 %left, %right"},
		{name: "left shift", op: "<<", typeID: llvmTypes.u8, instruction: " = shl i8 %left, %right", shift: true},
		{name: "signed right shift", op: ">>", typeID: llvmTypes.i8, instruction: " = ashr i8 %left, %right", shift: true},
		{name: "unsigned right shift", op: ">>", typeID: llvmTypes.u8, instruction: " = lshr i8 %left, %right", shift: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &mir.RefName{Name: "result", Type: tt.typeID}
			mod := &mir.Module{
				Name: "test", Types: llvmTypes.table,
				Funcs: []*mir.Function{{
					Name:       "apply",
					Params:     []ir.Param{{Name: "left", Type: tt.typeID}, {Name: "right", Type: tt.typeID}},
					ReturnType: tt.typeID,
					Blocks: []*mir.Block{{
						ID: 0,
						Instrs: []mir.Instr{&mir.Assign{Name: "result", Value: &mir.Binary{
							Op: tt.op, Left: &mir.RefName{Name: "left", Type: tt.typeID}, Right: &mir.RefName{Name: "right", Type: tt.typeID}, Type: tt.typeID,
						}}},
						Term: &mir.Ret{Value: result},
					}},
				}},
			}
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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

func TestGenerateLLVMIRGuardsMixedShiftCountBeforeCast(t *testing.T) {
	u16 := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeInteger, Bits: 16})
	result := &mir.RefName{Name: "result", Type: llvmTypes.u8}
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "apply",
			Params:     []ir.Param{{Name: "left", Type: llvmTypes.u8}, {Name: "right", Type: u16}},
			ReturnType: llvmTypes.u8,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "result", Value: &mir.Binary{
					Op: ">>", Left: &mir.RefName{Name: "left", Type: llvmTypes.u8}, Right: &mir.RefName{Name: "right", Type: u16}, Type: llvmTypes.u8,
				}}},
				Term: &mir.Ret{Value: result},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	guard := strings.Index(out, "icmp uge i16 %right, 8")
	cast := strings.Index(out, "trunc i16 %right to i8")
	shift := strings.Index(out, " = lshr i8 %left,")
	if guard < 0 || cast < guard || shift < cast {
		t.Fatalf("mixed shift count must guard before cast and shift, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersIntegerComplement(t *testing.T) {
	result := &mir.RefName{Name: "result", Type: llvmTypes.u8}
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "complement",
			Params:     []ir.Param{{Name: "value", Type: llvmTypes.u8}},
			ReturnType: llvmTypes.u8,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "result", Value: &mir.Unary{
					Op: "~", Arg: &mir.RefName{Name: "value", Type: llvmTypes.u8}, Type: llvmTypes.u8,
				}}},
				Term: &mir.Ret{Value: result},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, " = xor i8 %value, -1") {
		t.Fatalf("expected finite-width complement, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersOwnedPointerDrop(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "release",
			Params:     []ir.Param{{Name: "value", Type: llvmTypes.ownedI32}},
			ReturnType: llvmTypes.void,
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: llvmTypes.ownedI32}}},
				Term:   &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, "extractvalue { i32*, i8* } %value, 1") ||
		!strings.Contains(out, "extractvalue { i32*, i8* } %value, 0") ||
		!strings.Contains(out, "ptrtoint i32* getelementptr (i32, i32* null, i32 1) to i64") ||
		!strings.Contains(out, "@peeper_default_free_fn") {
		t.Fatalf("expected owned-pointer deallocation through allocator, got:\n%s", out)
	}
}

func TestGenerateLLVMIRReusesExistingFreeDeclaration(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{
			{Name: "free", Params: []ir.Param{{Name: "value", Type: llvmTypes.rawptr}}, ReturnType: llvmTypes.void},
			{
				Name:       "release",
				Params:     []ir.Param{{Name: "value", Type: llvmTypes.ownedI32}},
				ReturnType: llvmTypes.void,
				Blocks: []*mir.Block{{
					ID:     0,
					Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: llvmTypes.ownedI32}}},
					Term:   &mir.Ret{},
				}},
			},
		},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if count := strings.Count(out, "declare void @free(i8*)"); count != 1 {
		t.Fatalf("expected one free declaration, got %d:\n%s", count, out)
	}
	if !strings.Contains(out, "call void @free(i8*") {
		t.Fatalf("expected automatic destruction to reuse free declaration, got:\n%s", out)
	}
}

func TestGenerateLLVMIRRejectsIncompatibleFreeDeclaration(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{
			{Name: "free", Params: []ir.Param{{Name: "value", Type: llvmTypes.i32}}, ReturnType: llvmTypes.void},
			{
				Name:       "release",
				Params:     []ir.Param{{Name: "value", Type: llvmTypes.ownedI32}},
				ReturnType: llvmTypes.void,
				Blocks: []*mir.Block{{
					ID:     0,
					Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: llvmTypes.ownedI32}}},
					Term:   &mir.Ret{},
				}},
			},
		},
	}
	diag := diagnostics.NewDiagnosticBag()
	out := GenerateLLVMIR(mod, diag, testLinuxAMD64, false)
	if out != "" {
		t.Fatalf("expected incompatible free ABI to suppress LLVM output, got:\n%s", out)
	}
	if !strings.Contains(diag.EmitAllToString(), "runtime symbol `free` must have signature fn(rawptr) -> void") {
		t.Fatalf("expected incompatible free ABI diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestGenerateLLVMIRLowersDynamicArrayAllocation(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "values",
			ReturnType: llvmTypes.dynamicI32,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "values", Value: &mir.DynamicArrayAlloc{
					Length: 3,
					Type:   llvmTypes.dynamicI32,
				}}},
				Term: &mir.Ret{Value: &mir.RefName{Name: "values", Type: llvmTypes.dynamicI32}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	for _, expected := range []string{
		"declare i8* @malloc(i64)",
		"@llvm.umul.with.overflow.i64",
		"ptrtoint i32* getelementptr",
		"select i1",
		"i64 1, i64",
		"call i8* @malloc(i64",
		"icmp eq i8*",
		"call void @llvm.trap()",
		"insertvalue { i32*, i64, i64, i8* }",
		"i64 3, 2",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in dynamic allocation IR:\n%s", expected, out)
		}
	}
}

func TestGenerateLLVMIRLowersEmptyDynamicArrayWithoutAllocation(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "values",
			ReturnType: llvmTypes.dynamicI32,
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Assign{Name: "values", Value: &mir.DynamicArrayAlloc{Type: llvmTypes.dynamicI32}}},
				Term:   &mir.Ret{Value: &mir.RefName{Name: "values", Type: llvmTypes.dynamicI32}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if strings.Count(out, "call i8* @malloc") != 1 || strings.Contains(out, "umul.with.overflow") {
		t.Fatalf("empty dynamic array must not allocate storage:\n%s", out)
	}
	if !strings.Contains(out, "ret { i32*, i64, i64, i8* }") || !strings.Contains(out, "i64 0, 2") {
		t.Fatalf("empty dynamic array must return zero-length header:\n%s", out)
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
			value: &mir.RefName{Name: "value", Type: llvmTypes.i32},
			expected: []string{
				"array_append_capacity_", "@llvm.umul.with.overflow.i64", "array_relocate_loop_",
				"store i32 %value", "call void @free(i8*",
			},
		},
		{
			name:   "reserve",
			op:     symbols.CompilerOpReserve,
			length: &mir.RefName{Name: "size", Type: llvmTypes.usize},
			expected: []string{
				"icmp uge i64", "array_reserve_reuse_", "array_relocate_loop_", "call i8* @malloc(i64", "call void @free(i8*",
			},
		},
		{
			name:   "resize",
			op:     symbols.CompilerOpResize,
			length: &mir.RefName{Name: "size", Type: llvmTypes.usize},
			value:  &mir.RefName{Name: "value", Type: llvmTypes.i32},
			expected: []string{
				"array_resize_loop_", "icmp ult i64", "store i32 %value", "insertvalue { i32*, i64, i64, i8* }",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := dynamicArrayOperationModule(llvmTypes, tt.name, llvmTypes.dynamicI32, tt.op, tt.length, tt.value)
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
			for _, expected := range append([]string{
				"declare i8* @malloc(i64)", "declare void @free(i8*)", "store { i32*, i64, i64, i8* }",
			}, tt.expected...) {
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
		arrayType ir.TypeID
		expected  []string
	}{
		{name: "scalar", arrayType: llvmTypes.dynamicI32, expected: []string{"array_shrink_drop_", "icmp ult i64", "array_shrink_done_"}},
		{name: "owner", arrayType: llvmTypes.dynamicDynamicI32, expected: []string{"drop_array_loop_", "icmp ugt i64", "call void @free(i8*"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := dynamicArrayOperationModule(llvmTypes, tt.name, tt.arrayType, symbols.CompilerOpShrink, &mir.RefName{Name: "size", Type: llvmTypes.usize}, nil)
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
			if tt.name == "scalar" && strings.Contains(out, "umul.with.overflow") {
				t.Fatalf("shrink must not calculate storage size:\n%s", out)
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
	tests := []struct {
		name   string
		op     symbols.CompilerOp
		length bool
		value  bool
	}{
		{name: "append", op: symbols.CompilerOpAppend, value: true},
		{name: "reserve", op: symbols.CompilerOpReserve, length: true},
		{name: "resize", op: symbols.CompilerOpResize, length: true, value: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			types := newLLVMTypeFixture(target.Bits32)
			var length, value mir.ValueRef
			if tt.length {
				length = &mir.RefName{Name: "size", Type: types.usize}
			}
			if tt.value {
				value = &mir.RefName{Name: "value", Type: types.i32}
			}
			mod := dynamicArrayOperationModule(types, tt.name, types.dynamicI32, tt.op, length, value)
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinux386, false)
			for _, expected := range []string{"@llvm.umul.with.overflow.i32", "call i8* %"} {
				if !strings.Contains(out, expected) {
					t.Fatalf("expected %q in 32-bit %s IR:\n%s", expected, tt.name, out)
				}
			}
			if strings.Contains(out, "icmp ugt i64") || strings.Contains(out, "trunc i64") {
				t.Fatalf("32-bit storage size must stay target-sized in %s IR:\n%s", tt.name, out)
			}
		})
	}
}

func TestGenerateLLVMIRLowersDynamicArrayShrinkFor32BitTarget(t *testing.T) {
	types := newLLVMTypeFixture(target.Bits32)
	mod := dynamicArrayOperationModule(types, "shrink", types.dynamicI32, symbols.CompilerOpShrink, &mir.RefName{Name: "size", Type: types.usize}, nil)
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinux386, false)
	if strings.Contains(out, "zext i32 %size to i64") {
		t.Fatalf("32-bit usize must stay i32 in shrink IR:\n%s", out)
	}
	if strings.Contains(out, "umul.with.overflow") {
		t.Fatalf("32-bit shrink must not calculate storage size:\n%s", out)
	}
}

func dynamicArrayOperationModule(types llvmTypeFixture, name string, arrayType ir.TypeID, op symbols.CompilerOp, length, value mir.ValueRef) *mir.Module {
	ownerRefType := types.table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: arrayType})
	params := []ir.Param{{Name: "values", Type: ownerRefType}}
	if length != nil {
		params = append(params, ir.Param{Name: "size", Type: types.usize})
	}
	if value != nil {
		params = append(params, ir.Param{Name: "value", Type: types.i32})
	}
	return &mir.Module{Name: "test", Types: types.table, Funcs: []*mir.Function{{
		Name: name, Params: params, ReturnType: types.void, Blocks: []*mir.Block{{
			ID: 0,
			Instrs: []mir.Instr{&mir.DynamicArrayOp{
				Op: op, Array: &mir.RefName{Name: "values", Type: ownerRefType}, Length: length, Value: value, ArrayType: arrayType,
			}},
			Term: &mir.Ret{},
		}},
	}}}
}

func TestGenerateLLVMIRReusesCompatibleMallocDeclaration(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{
			{Name: "malloc", Params: []ir.Param{{Name: "size", Type: llvmTypes.usize}}, ReturnType: llvmTypes.rawptr},
			{
				Name:       "values",
				ReturnType: llvmTypes.dynamicI32,
				Blocks: []*mir.Block{{
					ID:     0,
					Instrs: []mir.Instr{&mir.Assign{Name: "values", Value: &mir.DynamicArrayAlloc{Length: 1, Type: llvmTypes.dynamicI32}}},
					Term:   &mir.Ret{Value: &mir.RefName{Name: "values", Type: llvmTypes.dynamicI32}},
				}},
			},
		},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if count := strings.Count(out, "declare i8* @malloc(i64)"); count != 1 {
		t.Fatalf("expected one malloc declaration, got %d:\n%s", count, out)
	}
}

func TestGenerateLLVMIRMallocUsesTargetSizedUsize(t *testing.T) {
	for _, tt := range []struct {
		name string
		info target.Info
		want string
	}{
		{name: "32-bit", info: testLinux386, want: "declare i8* @malloc(i32)"},
		{name: "64-bit", info: testLinuxAMD64, want: "declare i8* @malloc(i64)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			types := newLLVMTypeFixture(tt.info.IndexBits)
			mod := dynamicArrayAllocModule(types)
			mod.Funcs = append([]*mir.Function{{Name: "malloc", Params: []ir.Param{{Name: "size", Type: types.usize}}, ReturnType: types.rawptr}}, mod.Funcs...)
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), tt.info, false)
			if !strings.Contains(out, tt.want) {
				t.Fatalf("expected %q, got:\n%s", tt.want, out)
			}
		})
	}
}

func dynamicArrayAllocModule(types llvmTypeFixture) *mir.Module {
	return &mir.Module{
		Name: "test", Types: types.table,
		Funcs: []*mir.Function{{
			Name:       "values",
			ReturnType: types.dynamicI32,
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Assign{Name: "values", Value: &mir.DynamicArrayAlloc{Length: 1, Type: types.dynamicI32}}},
				Term:   &mir.Ret{Value: &mir.RefName{Name: "values", Type: types.dynamicI32}},
			}},
		}},
	}
}

func TestGenerateLLVMIRRejectsIncompatibleMallocDeclaration(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{
			{Name: "malloc", Params: []ir.Param{{Name: "size", Type: llvmTypes.i32}}, ReturnType: llvmTypes.rawptr},
			{
				Name:       "values",
				ReturnType: llvmTypes.dynamicI32,
				Blocks: []*mir.Block{{
					ID:     0,
					Instrs: []mir.Instr{&mir.Assign{Name: "values", Value: &mir.DynamicArrayAlloc{Length: 1, Type: llvmTypes.dynamicI32}}},
					Term:   &mir.Ret{Value: &mir.RefName{Name: "values", Type: llvmTypes.dynamicI32}},
				}},
			},
		},
	}
	diag := diagnostics.NewDiagnosticBag()
	out := GenerateLLVMIR(mod, diag, testLinuxAMD64, false)
	if out != "" {
		t.Fatalf("expected incompatible malloc ABI to suppress LLVM output, got:\n%s", out)
	}
	if !strings.Contains(diag.EmitAllToString(), "runtime symbol `malloc` must have signature fn(usize) -> rawptr") {
		t.Fatalf("expected incompatible malloc ABI diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestGenerateLLVMIRDropsNestedOwnersBeforeStorage(t *testing.T) {
	child := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeStruct, Fields: []ir.TypeField{{Name: "child", Type: llvmTypes.ownedI32}}})
	typeID := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: child})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "release",
			Params:     []ir.Param{{Name: "value", Type: typeID}},
			ReturnType: llvmTypes.void,
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: typeID}}},
				Term:   &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if count := strings.Count(out, "ptrtoint i32* getelementptr (i32, i32* null, i32 1) to i64"); count != 1 {
		t.Fatalf("expected child size computation, got %d:\n%s", count, out)
	}
	if !strings.Contains(out, "@peeper_default_free_fn") {
		t.Fatalf("expected deallocation through descriptor, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersOwnedPointerStructLayout(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "pass",
			Params:     []ir.Param{{Name: "value", Type: llvmTypes.ownedI32}},
			ReturnType: llvmTypes.ownedI32,
			Blocks: []*mir.Block{{
				ID:   0,
				Term: &mir.Ret{Value: &mir.RefName{Name: "value", Type: llvmTypes.ownedI32}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, "define { i32*, i8* } @pass({ i32*, i8* } %value)") {
		t.Fatalf("expected owned pointer struct ABI {T*, i8*}, got:\n%s", out)
	}
	if !strings.Contains(out, "ret { i32*, i8* } %value") {
		t.Fatalf("expected owned pointer struct return, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersOptionalOwnedPointerAsTagged(t *testing.T) {
	optionalOwnedI32 := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOptional, Elem: llvmTypes.ownedI32})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "nullable",
			Params:     []ir.Param{{Name: "opt", Type: optionalOwnedI32}},
			ReturnType: optionalOwnedI32,
			Blocks: []*mir.Block{{
				ID:   0,
				Term: &mir.Ret{Value: &mir.RefName{Name: "opt", Type: optionalOwnedI32}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, "define { i1, { i32*, i8* } } @nullable({ i1, { i32*, i8* } }") {
		t.Fatalf("expected tagged optional owned pointer ABI, got:\n%s", out)
	}
}

func TestGenerateLLVMIRDefaultDescriptorEmitted(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "drop",
			Params:     []ir.Param{{Name: "value", Type: llvmTypes.ownedI32}},
			ReturnType: llvmTypes.void,
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: llvmTypes.ownedI32}}},
				Term:   &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, "@peeper_default_alloc = private constant [3 x i8*]") {
		t.Fatalf("expected default descriptor global, got:\n%s", out)
	}
	if !strings.Contains(out, "@peeper_default_alloc_fn") || !strings.Contains(out, "@peeper_default_free_fn") {
		t.Fatalf("expected default descriptor thunks, got:\n%s", out)
	}
}

func TestGenerateLLVMIRNoDescriptorWithoutOwnedPointers(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "main",
			ReturnType: llvmTypes.i32,
			Blocks: []*mir.Block{{
				ID:   0,
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if strings.Contains(out, "@peeper_default_alloc") {
		t.Fatalf("unexpected default descriptor without owned pointers, got:\n%s", out)
	}
}

func TestGenerateLLVMIRBorrowedScalarShrinkDoesNotReserveAllocatorRuntime(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{
			{
				Name:       "free",
				Params:     []ir.Param{{Name: "value", Type: llvmTypes.i32}},
				ReturnType: llvmTypes.void,
			},
			{
				Name:       "shorten",
				Params:     []ir.Param{{Name: "values", Type: llvmTypes.mutRefDynamicI32}, {Name: "size", Type: llvmTypes.usize}},
				ReturnType: llvmTypes.void,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{&mir.DynamicArrayOp{
						Op:        symbols.CompilerOpShrink,
						Array:     &mir.RefName{Name: "values", Type: llvmTypes.mutRefDynamicI32},
						Length:    &mir.RefName{Name: "size", Type: llvmTypes.usize},
						ArrayType: llvmTypes.dynamicI32,
					}},
					Term: &mir.Ret{},
				}},
			},
		},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, "declare void @free(i32)") {
		t.Fatalf("expected unrelated free declaration, got:\n%s", out)
	}
	if strings.Contains(out, "@peeper_default_alloc") || strings.Contains(out, "declare i8* @malloc") || strings.Contains(out, "declare void @free(i8*)") {
		t.Fatalf("carrier-only pass-through must not reserve allocator runtime, got:\n%s", out)
	}
}

func TestGenerateLLVMIRRejectsOwnerBearingExtern(t *testing.T) {
	for _, tc := range []struct {
		name       string
		params     []ir.Param
		returnType ir.TypeID
	}{
		{name: "owner return", params: []ir.Param{{Name: "size", Type: llvmTypes.usize}}, returnType: llvmTypes.ownedI32},
		{name: "owner parameter", params: []ir.Param{{Name: "value", Type: llvmTypes.ownedI32}}, returnType: llvmTypes.void},
		{name: "nested owner", params: []ir.Param{{Name: "value", Type: llvmTypes.table.Intern(ir.Type{Kind: ir.TypeStruct, Fields: []ir.TypeField{{Name: "value", Type: llvmTypes.ownedI32}}})}}, returnType: llvmTypes.void},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mod := &mir.Module{
				Name: "test", Types: llvmTypes.table,
				Funcs: []*mir.Function{{
					Name:       "foreign",
					Params:     tc.params,
					ReturnType: tc.returnType,
				}},
			}
			diag := diagnostics.NewDiagnosticBag()
			if out := GenerateLLVMIR(mod, diag, testLinuxAMD64, false); out != "" {
				t.Fatalf("owner-bearing extern must suppress LLVM output, got:\n%s", out)
			}
			if !diag.HasErrors() {
				t.Fatal("owner-bearing extern must emit diagnostic")
			}
		})
	}
}

func TestGenerateLLVMIRAcceptsRawExternBoundaries(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{
			{Name: "malloc", Params: []ir.Param{{Name: "size", Type: llvmTypes.usize}}, ReturnType: llvmTypes.rawptr},
			{Name: "free", Params: []ir.Param{{Name: "value", Type: llvmTypes.rawptr}}, ReturnType: llvmTypes.void},
			{Name: "puts", Params: []ir.Param{{Name: "value", Type: llvmTypes.cstr}}, ReturnType: llvmTypes.i32},
		},
	}
	if out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false); out == "" {
		t.Fatal("rawptr and cstr extern boundaries must remain valid")
	}
}

func TestGenerateLLVMIRUsesCarriedAllocatorForInterfaceDrops(t *testing.T) {
	iface := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeInterface, Methods: []ir.TypeMethod{{Name: "take", Params: []ir.TypeField{{Name: "self", Type: llvmTypes.valueStruct}}, Return: llvmTypes.void}}})
	ownedIface := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: iface})
	optionalOwnedIface := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOptional, Elem: ownedIface})
	for _, tt := range []struct {
		name   string
		typeID ir.TypeID
	}{{"owned", ownedIface}, {"optional owned", optionalOwnedIface}} {
		t.Run(tt.name, func(t *testing.T) {
			mod := &mir.Module{
				Name: "test", Types: llvmTypes.table,
				Funcs: []*mir.Function{{
					Name:       "release",
					Params:     []ir.Param{{Name: "value", Type: tt.typeID}},
					ReturnType: llvmTypes.void,
					Blocks: []*mir.Block{{
						ID:     0,
						Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: tt.typeID}}},
						Term:   &mir.Ret{},
					}},
				}},
			}
			out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
			if strings.Contains(out, "@peeper_default_alloc") || !strings.Contains(out, "void (i8*, i8*)*") {
				t.Fatalf("interface drop must use its carried allocator release, got:\n%s", out)
			}
		})
	}
}

func TestGenerateLLVMIROwnedInterfaceAdoptsAllocationAndDropsPayload(t *testing.T) {
	payload := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeStruct, Fields: []ir.TypeField{{Name: "child", Type: llvmTypes.ownedI32}}})
	ownedPayload := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: payload})
	iface := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeInterface})
	ownedIface := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: iface})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "release",
			Params:     []ir.Param{{Name: "resource", Type: ownedPayload}},
			ReturnType: llvmTypes.void,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{
					&mir.Assign{Name: "erased", Value: &mir.InterfaceMake{
						Value:    &mir.RefName{Name: "resource", Type: ownedPayload},
						DataType: payload,
						Type:     ownedIface,
					}},
					&mir.Drop{Value: &mir.RefName{Name: "erased", Type: ownedIface}},
				},
				Term: &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if strings.Contains(out, "alloca { { i32*, i8* } }") {
		t.Fatalf("owned interface conversion must adopt existing allocation, got:\n%s", out)
	}
	if !strings.Contains(out, "private constant [2 x i8*]") ||
		!strings.Contains(out, "define void @__iface_drop") ||
		!strings.Contains(out, "define void @__iface_release") ||
		!strings.Contains(out, "bitcast { { i32*, i8* } }* %") {
		t.Fatalf("expected direct fat carrier with payload-drop slot, got:\n%s", out)
	}
	if count := strings.Count(out, "@peeper_default_free_fn"); count < 2 {
		t.Fatalf("expected nested payload and carrier storage deallocs through descriptor, got %d:\n%s", count, out)
	}
}

func TestGenerateLLVMIRInterfaceMethodUsesSlotAfterDrop(t *testing.T) {
	iface := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeInterface, Methods: []ir.TypeMethod{{Name: "read", Params: []ir.TypeField{{Name: "self", Type: llvmTypes.refValueStruct}}, Return: llvmTypes.i32}}})
	interfaceType := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: iface})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{
			{
				Name:       "read_thunk",
				Params:     []ir.Param{{Name: "data", Type: llvmTypes.rawptr}},
				ReturnType: llvmTypes.i32,
			},
			{
				Name:       "read",
				Params:     []ir.Param{{Name: "counter", Type: llvmTypes.refValueStruct}},
				ReturnType: llvmTypes.i32,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "reader", Value: &mir.InterfaceMake{
							Value:    &mir.RefName{Name: "counter", Type: llvmTypes.refValueStruct},
							DataType: llvmTypes.valueStruct,
							Slots:    []mir.ValueRef{&mir.RefName{Name: "read_thunk", Type: llvmTypes.fnRawptrI32}},
							Type:     interfaceType,
						}},
						&mir.Assign{Name: "result", Value: &mir.InterfaceCall{
							Base: &mir.RefName{Name: "reader", Type: interfaceType},
							Slot: 0,
							Type: llvmTypes.i32,
						}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "result", Type: llvmTypes.i32}},
				}},
			},
		},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, "private constant [2 x i8*]") ||
		!strings.Contains(out, "getelementptr inbounds i8*, i8**") ||
		!strings.Contains(out, "i32 1") {
		t.Fatalf("expected method dispatch after payload-drop slot, got:\n%s", out)
	}
}

func TestGenerateLLVMIRInterfaceThunkUsesActualInterfaceReceiverType(t *testing.T) {
	interfaceType := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeInterface, Methods: []ir.TypeMethod{{Name: "read", Params: []ir.TypeField{{Name: "self", Type: llvmTypes.refValueStruct}}, Return: llvmTypes.i32}}})
	functionType := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeFunction, Params: []ir.TypeID{interfaceType}, Return: llvmTypes.i32})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		InterfaceThunks: []*mir.InterfaceThunk{{
			Name:     "interface_thunk",
			SlotType: llvmTypes.fnRawptrI32,
			FuncName: "consume_interface",
			FuncType: functionType,
			DataType: llvmTypes.valueStruct,
		}},
		Funcs: []*mir.Function{{
			Name:       "consume_interface",
			Params:     []ir.Param{{Name: "value", Type: interfaceType}},
			ReturnType: llvmTypes.i32,
			Blocks: []*mir.Block{{
				ID:   0,
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
			}},
		}},
	}

	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, "bitcast i8* %p0 to { i8*, i8* }*") ||
		!strings.Contains(out, "load { i8*, i8* }, { i8*, i8* }*") {
		t.Fatalf("interface thunk must load actual interface receiver type, got:\n%s", out)
	}
}

func TestInterfaceSymbolsDistinguishOwnedAndBorrowedABI(t *testing.T) {
	iface := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeInterface, Methods: []ir.TypeMethod{{Name: "read", Params: []ir.TypeField{{Name: "self", Type: llvmTypes.refValueStruct}}, Return: llvmTypes.i32}}})
	owned := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: iface})
	borrowed := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: iface})

	if interfaceSymbolName("itab", llvmTypes.table, owned, llvmTypes.valueStruct) == interfaceSymbolName("itab", llvmTypes.table, borrowed, llvmTypes.valueStruct) ||
		interfaceSymbolName("iface_drop", llvmTypes.table, owned, llvmTypes.valueStruct) == interfaceSymbolName("iface_drop", llvmTypes.table, borrowed, llvmTypes.valueStruct) {
		t.Fatal("owned and borrowed interface symbols must not share ABI names")
	}
}

func TestGenerateLLVMIRDropsDynamicArrayElementsInReverse(t *testing.T) {
	typeID := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeArray, Elem: llvmTypes.ownedI32})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "release",
			Params:     []ir.Param{{Name: "values", Type: typeID}},
			ReturnType: llvmTypes.void,
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "values", Type: typeID}}},
				Term:   &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	decrement := strings.Index(out, " = sub i64 ")
	elementLoad := strings.Index(out, " = getelementptr { i32*, i8* }, { i32*, i8* }* ")
	if !strings.Contains(out, " = icmp ugt i64 ") || decrement < 0 || elementLoad < decrement {
		t.Fatalf("expected reverse dynamic-array drop loop, got:\n%s", out)
	}
	if strings.Contains(out, " = phi i64 [ 0,") || strings.Contains(out, " = add i64 ") {
		t.Fatalf("dynamic-array drop must not advance from index zero, got:\n%s", out)
	}
}

func TestGenerateLLVMIRDropsStringThroughAllocator(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "release",
			Params:     []ir.Param{{Name: "value", Type: llvmTypes.stringType}},
			ReturnType: llvmTypes.void,
			Blocks: []*mir.Block{{
				ID:     0,
				Instrs: []mir.Instr{&mir.Drop{Value: &mir.RefName{Name: "value", Type: llvmTypes.stringType}}},
				Term:   &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	for _, expected := range []string{
		"extractvalue { i8*, i64, i8* } %value, 2",
		"ptrtoint i8* getelementptr (i8, i8* null, i32 1) to i64",
		"i32 1)",
		"call void %",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in string drop IR:\n%s", expected, out)
		}
	}
}

func TestGenerateLLVMIRLowersStringCharsFromBorrowedView(t *testing.T) {
	charType := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeChar})
	dynamicChar := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeArray, Elem: charType})
	refString := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: llvmTypes.stringType})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "chars",
			Params:     []ir.Param{{Name: "text", Type: refString}},
			ReturnType: llvmTypes.void,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{
					&mir.Assign{Name: "chars", Value: &mir.StringChars{
						Value: &mir.RefName{Name: "text", Type: refString}, Type: dynamicChar,
					}},
					&mir.Drop{Value: &mir.RefName{Name: "chars", Type: dynamicChar}},
				},
				Term: &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	for _, expected := range []string{
		"extractvalue { i8*, i64 } %text, 0",
		"extractvalue { i8*, i64 } %text, 1",
		"utf8_count_loop_",
		"utf8_decode_two_",
		"utf8_decode_three_",
		"utf8_decode_four_",
		"call i8* %",
		"store i32 %",
		"insertvalue { i32*, i64, i64, i8* }",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in string chars IR:\n%s", expected, out)
		}
	}
	if strings.Contains(out, "load { i8*, i64, i8* }") {
		t.Fatalf("borrowed string view unexpectedly loaded as owned carrier:\n%s", out)
	}
}

func TestGenerateLLVMIRReturnsStringSliceViewByValue(t *testing.T) {
	refString := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: llvmTypes.stringType})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "Prefix",
			Params:     []ir.Param{{Name: "text", Type: refString}},
			ReturnType: refString,
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "view", Value: &mir.SliceView{
					Source:       &mir.Place{Root: &mir.RefName{Name: "text", Type: refString}, Type: refString},
					Start:        &mir.RefConst{Value: "0", Type: llvmTypes.i32},
					End:          &mir.RefConst{Value: "1", Type: llvmTypes.i32},
					EndExclusive: true,
					Type:         refString,
				}}},
				Term: &mir.Ret{Value: &mir.RefName{Name: "view", Type: refString}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	for _, expected := range []string{
		"define { i8*, i64 } @Prefix({ i8*, i64 } %text)",
		"ret { i8*, i64 } %",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in returned string view IR:\n%s", expected, out)
		}
	}
	for _, unexpected := range []string{
		"alloca { i8*, i64, i8* }",
		"ret { i8*, i64, i8* }*",
	} {
		if strings.Contains(out, unexpected) {
			t.Fatalf("unexpected callee-local string carrier %q:\n%s", unexpected, out)
		}
	}
}

func TestGenerateLLVMIRLowersBoundedStringSliceViewFor32BitTarget(t *testing.T) {
	types := newLLVMTypeFixture(target.Bits32)
	refString := types.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: types.stringType})
	mod := &mir.Module{
		Name: "test", Types: types.table,
		Funcs: []*mir.Function{{
			Name:       "Prefix",
			Params:     []ir.Param{{Name: "text", Type: refString}},
			ReturnType: refString,
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "view", Value: &mir.SliceView{
					Source:       &mir.Place{Root: &mir.RefName{Name: "text", Type: refString}, Type: refString},
					Start:        &mir.RefConst{Value: "0", Type: types.i32},
					End:          &mir.RefConst{Value: "2", Type: types.i32},
					EndExclusive: true,
					Type:         refString,
				}}},
				Term: &mir.Ret{Value: &mir.RefName{Name: "view", Type: refString}},
			}},
		}},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinux386, false)
	for _, expected := range []string{
		"define { i8*, i32 } @Prefix({ i8*, i32 } %text)",
		"zext i32",
		"icmp ugt i64",
		"ret { i8*, i32 } %",
	} {
		if !strings.Contains(irText, expected) {
			t.Fatalf("expected %q in 32-bit string range IR:\n%s", expected, irText)
		}
	}

	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang unavailable for LLVM IR validation")
	}
	cmd := exec.Command(clang, "-target", testLinux386.LLVMTriple, "-x", "ir", "-c", "-o", filepath.Join(t.TempDir(), "prefix.o"), "-")
	cmd.Stdin = strings.NewReader(irText)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("32-bit bounded string range LLVM IR is invalid: %v\n%s\n%s", err, out, irText)
	}
}

func TestGenerateLLVMIRLowersDynamicArraySliceViewAcrossTargets(t *testing.T) {
	mod := &mir.Module{
		Name:     "test",
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{{
			Name:       "borrow",
			Params:     []ir.Param{{Name: "xs", Type: llvmTypes.dynamicI32}},
			ReturnType: llvmTypes.i32,
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "view", Value: &mir.SliceView{
					Source: &mir.Place{Root: &mir.RefName{Name: "xs", Type: llvmTypes.dynamicI32}, Type: llvmTypes.dynamicI32},
					Type:   llvmTypes.refSliceI32,
				}}},
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
			}},
		}},
	}
	targets := []struct {
		name string
		info target.Info
	}{
		{name: "linux", info: testLinuxAMD64},
		{name: "darwin", info: testDarwinARM64},
		{name: "windows", info: testWindowsAMD64},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), target.info, false)
			if !strings.Contains(irText, "extractvalue { i32*, i64, i64, i8* } %xs, 0") ||
				!strings.Contains(irText, "extractvalue { i32*, i64, i64, i8* } %xs, 1") {
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

func TestGenerateLLVMIRLowersBorrowedDynamicArraySliceViewAcrossTargets(t *testing.T) {
	targets := []struct {
		name string
		info target.Info
		bits int
	}{
		{name: "amd64", info: testLinuxAMD64, bits: target.Bits64},
		{name: "386", info: testLinux386, bits: target.Bits32},
	}
	for _, compilerTarget := range targets {
		t.Run(compilerTarget.name, func(t *testing.T) {
			types := newLLVMTypeFixture(compilerTarget.bits)
			indexType := "i64"
			if compilerTarget.bits == target.Bits32 {
				indexType = "i32"
			}
			header := "{ i32*, " + indexType + ", " + indexType + ", i8* }"
			for _, sourceType := range []ir.TypeID{types.refDynamicI32, types.mutRefDynamicI32} {
				mod := &mir.Module{
					Name: "test", Types: types.table, FilePath: unixTestPath,
					Funcs: []*mir.Function{{
						Name: "borrow", Params: []ir.Param{{Name: "xs", Type: sourceType}}, ReturnType: types.i32,
						Blocks: []*mir.Block{{
							ID: 0,
							Instrs: []mir.Instr{&mir.Assign{Name: "view", Value: &mir.SliceView{
								Source: &mir.Place{Root: &mir.RefName{Name: "xs", Type: sourceType}, Type: sourceType},
								Type:   types.refSliceI32,
							}}},
							Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: types.i32}},
						}},
					}},
				}
				irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), compilerTarget.info, false)
				load := "load " + header + ", " + header + "* %xs"
				loadAt := strings.Index(irText, load)
				extractAt := strings.Index(irText, "extractvalue "+header)
				if loadAt < 0 || extractAt < loadAt {
					t.Fatalf("borrowed owner header must load before field extraction, got:\n%s", irText)
				}

				clang, err := exec.LookPath("clang")
				if err != nil {
					continue
				}
				cmd := exec.Command(clang, "-target", compilerTarget.info.LLVMTriple, "-x", "ir", "-c", "-o", filepath.Join(t.TempDir(), "slice.o"), "-")
				cmd.Stdin = strings.NewReader(irText)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("borrowed owner slice LLVM IR is invalid: %v\n%s\n%s", err, out, irText)
				}
			}
		})
	}
}

func TestGenerateLLVMIRLowersCheckedInclusiveFixedArraySlice(t *testing.T) {
	mod := &mir.Module{
		Name:     "test",
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{{
			Name:       "slice",
			Params:     []ir.Param{{Name: "xs", Type: llvmTypes.mutRefFixed4I32}},
			ReturnType: llvmTypes.i32,
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "view", Value: &mir.SliceView{
					Source: &mir.Place{Root: &mir.RefName{Name: "xs", Type: llvmTypes.mutRefFixed4I32}, Type: llvmTypes.mutRefFixed4I32},
					Start:  &mir.RefConst{Value: "1", Type: llvmTypes.i32},
					End:    &mir.RefConst{Value: "2", Type: llvmTypes.i32},
					Type:   llvmTypes.mutRefSliceI32,
				}}},
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
			}},
		}},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "slice",
			Params:     []ir.Param{{Name: "xs", Type: llvmTypes.refSliceI32}},
			ReturnType: llvmTypes.i32,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "view", Value: &mir.SliceView{
					Source:       &mir.Place{Root: &mir.RefName{Name: "xs", Type: llvmTypes.refSliceI32}, Type: llvmTypes.refSliceI32},
					End:          &mir.RefConst{Value: "2", Type: llvmTypes.u8},
					EndExclusive: true,
					Type:         llvmTypes.refSliceI32,
				}}},
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
			}},
		}},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 0") ||
		!strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 1") {
		t.Fatalf("expected shared view data and length extraction, got:\n%s", irText)
	}
	if strings.Contains(irText, "{ i32*, i64, i64, i8* }") {
		t.Fatalf("reslicing must not recover capacity, got:\n%s", irText)
	}
	trap := strings.Index(irText, "call void @llvm.trap()")
	ready := strings.Index(irText, "\nslice_bounds_ready_")
	gep := strings.Index(irText, "getelementptr i32")
	if strings.Count(irText, "icmp ugt i64") < 2 || trap < 0 || ready < trap || gep < ready {
		t.Fatalf("exclusive and reversed bounds checks must dominate adjusted GEP, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLowersZeroValueOptionals(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := &mir.Module{
		Name:     "test",
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "tagged",
				ReturnType: llvmTypes.optionalI32,
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "x", Value: &mir.ZeroValue{Type: llvmTypes.optionalI32}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "x", Type: llvmTypes.optionalI32}},
				}},
			},
			{
				Name:       "niche",
				ReturnType: llvmTypes.optionalOwnedI32,
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "p", Value: &mir.ZeroValue{Type: llvmTypes.optionalOwnedI32}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "p", Type: llvmTypes.optionalOwnedI32}},
				}},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "tagged",
				ReturnType: llvmTypes.optionalI32,
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "x", Value: &mir.OptionalSome{Value: &mir.RefConst{Value: "7", Type: llvmTypes.i32}, Type: llvmTypes.optionalI32}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "x", Type: llvmTypes.optionalI32}},
				}},
			},
			{
				Name:       "niche",
				Params:     []ir.Param{{Name: "p", Type: llvmTypes.ownedI32}},
				ReturnType: llvmTypes.optionalOwnedI32,
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "x", Value: &mir.OptionalSome{Value: &mir.RefName{Name: "p", Type: llvmTypes.ownedI32}, Type: llvmTypes.optionalOwnedI32}},
					},
					Term: &mir.Ret{Value: &mir.RefName{Name: "x", Type: llvmTypes.optionalOwnedI32}},
				}},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: llvmTypes.i32,
				EntryID:    0,
				Blocks: []*mir.Block{{
					ID: 0,
					Instrs: []mir.Instr{
						&mir.Assign{Name: "x", Value: &mir.OptionalSome{Value: &mir.RefConst{Value: "7", Type: llvmTypes.i32}, Type: llvmTypes.optionalI32}},
						&mir.Assign{Name: "none", Value: &mir.ZeroValue{Type: llvmTypes.optionalI32}},
						&mir.Assign{Name: "isnone", Value: &mir.Binary{
							Op:    "==",
							Left:  &mir.RefName{Name: "x", Type: llvmTypes.optionalI32},
							Right: &mir.RefName{Name: "none", Type: llvmTypes.optionalI32},
							Type:  llvmTypes.boolType,
						}},
					},
					Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
				}},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: llvmTypes.i32,
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "n", Value: &mir.Move{Src: &mir.RefConst{Value: "0", Type: llvmTypes.i32}, Type: llvmTypes.i32}},
						},
						Term: &mir.Jump{TargetID: 1},
					},
					{
						ID: 1,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "cond", Value: &mir.Binary{
								Op:    "<",
								Left:  &mir.RefName{Name: "n", Type: llvmTypes.i32},
								Right: &mir.RefConst{Value: "3", Type: llvmTypes.i32},
								Type:  llvmTypes.boolType,
							}},
						},
						Term: &mir.Branch{Cond: &mir.RefName{Name: "cond", Type: llvmTypes.boolType}, ThenID: 2, ElseID: 3},
					},
					{
						ID: 2,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "next", Value: &mir.Binary{
								Op:    "+",
								Left:  &mir.RefName{Name: "n", Type: llvmTypes.i32},
								Right: &mir.RefConst{Value: "1", Type: llvmTypes.i32},
								Type:  llvmTypes.i32,
							}},
							&mir.Assign{Name: "n", Value: &mir.Move{Src: &mir.RefName{Name: "next", Type: llvmTypes.i32}, Type: llvmTypes.i32}},
						},
						Term: &mir.Jump{TargetID: 1},
					},
					{
						ID:   3,
						Term: &mir.Ret{Value: &mir.RefName{Name: "n", Type: llvmTypes.i32}},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: llvmTypes.void,
				EntryID:    0,
				Location:   source.NewLocation(unixTestPath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{Name: "t1", Value: &mir.Call{
								Callee: &mir.RefName{Name: "write", Type: llvmTypes.fnI32},
								Type:   llvmTypes.i32,
							}, Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 12})},
						},
						Term: &mir.Ret{Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 8})},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
		Types:    llvmTypes.table,
		FilePath: windowsTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: llvmTypes.i32,
				EntryID:    0,
				Location:   source.NewLocation(windowsTestPath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Call{
								Callee:   &mir.RefName{Name: "Ping", Type: llvmTypes.fnVoid},
								Type:     llvmTypes.void,
								Location: source.NewLocation(windowsTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 8}),
							},
						},
						Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}, Location: source.NewLocation(windowsTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 10})},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testWindowsAMD64, false)
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
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: llvmTypes.i32,
				EntryID:    0,
				Location:   source.NewLocation(unixTestPath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Call{
								Callee:   &mir.RefName{Name: "Ping", Type: llvmTypes.fnVoid},
								Type:     llvmTypes.void,
								Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 8}),
							},
						},
						Term: &mir.Ret{
							Value:    &mir.RefConst{Value: "0", Type: llvmTypes.i32},
							Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 10}),
						},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, true)
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
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: llvmTypes.i32,
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
									Left:     &mir.RefConst{Value: "1", Type: llvmTypes.i32, Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 3})},
									Right:    &mir.RefConst{Value: "2", Type: llvmTypes.i32, Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     llvmTypes.i32,
									Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
								},
								Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7}),
							},
							&mir.Assign{
								Name: "t2",
								Value: &mir.Binary{
									Op:       "*",
									Left:     &mir.RefName{Name: "t1", Type: llvmTypes.i32, Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 7})},
									Right:    &mir.RefConst{Value: "3", Type: llvmTypes.i32, Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 3})},
									Type:     llvmTypes.i32,
									Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
								},
								Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7}),
							},
						},
						Term: &mir.Ret{
							Value:    &mir.RefName{Name: "t2", Type: llvmTypes.i32, Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 7})},
							Location: source.NewLocation(unixTestPath, source.Position{Line: 4, Column: 2}, source.Position{Line: 4, Column: 8}),
						},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, true)
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
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "main",
				ReturnType: llvmTypes.i32,
				EntryID:    0,
				Location:   source.NewLocation(unixTestPath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 10}),
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{
								Name: "cond",
								Value: &mir.Cast{
									Arg:      &mir.RefConst{Value: "1", Type: llvmTypes.i32, Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 6}, source.Position{Line: 2, Column: 7})},
									Type:     llvmTypes.boolType,
									Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 11}),
								},
								Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 11}),
							},
						},
						Term: &mir.Branch{
							Cond:     &mir.RefName{Name: "cond", Type: llvmTypes.boolType, Location: source.NewLocation(unixTestPath, source.Position{Line: 2, Column: 2}, source.Position{Line: 2, Column: 11})},
							ThenID:   1,
							ElseID:   2,
							Location: source.NewLocation(unixTestPath, source.Position{Line: 3, Column: 2}, source.Position{Line: 3, Column: 12}),
						},
					},
					{
						ID:   1,
						Term: &mir.Ret{Value: &mir.RefConst{Value: "1", Type: llvmTypes.i32}},
					},
					{
						ID:   2,
						Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
					},
				},
			},
		},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "icmp ne i32 1, 0") {
		t.Fatalf("expected explicit bool cast to lower as compare, got:\n%s", irText)
	}
	if strings.Contains(irText, "fcmp one") {
		t.Fatalf("unexpected float truthiness compare in integer bool cast, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLowersIndirectFieldPlaceWithoutTempAlloca(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	owned := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: llvmTypes.valueStruct})
	shared := llvmTypes.refValueStruct
	mutable := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: llvmTypes.valueStruct})
	for _, tt := range []struct {
		name   string
		typeID ir.TypeID
		owned  bool
	}{{"owned", owned, true}, {"shared", shared, false}, {"mutable", mutable, false}} {
		t.Run(tt.name, func(t *testing.T) {
			mod := &mir.Module{
				Name:     "test",
				Types:    llvmTypes.table,
				FilePath: unixTestPath,
				Funcs: []*mir.Function{
					{
						Name:       "main",
						Params:     []ir.Param{{Name: "box", Type: tt.typeID}},
						ReturnType: llvmTypes.i32,
						EntryID:    0,
						Blocks: []*mir.Block{
							{
								ID: 0,
								Instrs: []mir.Instr{
									&mir.Assign{
										Name: "fieldptr",
										Value: &mir.AddrOf{
											Place: &mir.Place{
												Root: &mir.RefName{Name: "box", Type: tt.typeID},
												Projections: []mir.PlaceProjection{
													{Kind: mir.PlaceProjectionDeref, Type: llvmTypes.valueStruct},
													{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: llvmTypes.i32},
												},
												Type: llvmTypes.i32,
											},
											Type: llvmTypes.mutRefI32,
										},
									},
								},
								Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
							},
						},
					},
				},
			}

			irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
			if tt.owned {
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

func TestGenerateLLVMIRLowersProjectedFieldRawAddressDirectly(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mutableStruct := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: llvmTypes.valueStruct})
	mod := &mir.Module{
		Name:     "test",
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{{
			Name:       "main",
			Params:     []ir.Param{{Name: "box", Type: mutableStruct}},
			ReturnType: llvmTypes.i32,
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{
					&mir.Assign{
						Name: "raw",
						Value: &mir.AddrOf{
							Place: &mir.Place{
								Root: &mir.RefName{Name: "box", Type: mutableStruct},
								Projections: []mir.PlaceProjection{
									{Kind: mir.PlaceProjectionDeref, Type: llvmTypes.valueStruct},
									{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: llvmTypes.i32},
								},
								Type: llvmTypes.i32,
							},
							Type: llvmTypes.rawptr,
						},
					},
				},
				Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
			}},
		}},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "getelementptr inbounds { i32 }, { i32 }* %box") ||
		!strings.Contains(irText, "bitcast i32*") || !strings.Contains(irText, "to i8*") {
		t.Fatalf("expected projected field address rawptr cast, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLowersOwnedStringStorageRawAddress(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "address",
			Params:     []ir.Param{{Name: "text", Type: llvmTypes.stringType}},
			ReturnType: llvmTypes.rawptr,
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "raw", Value: &mir.AddrOf{
					Place: &mir.Place{Root: &mir.RefName{Name: "text", Type: llvmTypes.stringType}, Type: llvmTypes.stringType},
					Type:  llvmTypes.rawptr,
				}}},
				Term: &mir.Ret{Value: &mir.RefName{Name: "raw", Type: llvmTypes.rawptr}},
			}},
		}},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "bitcast { i8*, i64, i8* }*") || !strings.Contains(irText, "to i8*") {
		t.Fatalf("expected owned string storage pointer cast to rawptr, got:\n%s", irText)
	}
	if strings.Contains(irText, "bitcast { i8*, i64, i8* } %") {
		t.Fatalf("owned string aggregate must not be bitcast to pointer, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRLowersBorrowedStringLengthWithoutDataExtraction(t *testing.T) {
	refString := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Elem: llvmTypes.stringType})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "length",
			Params:     []ir.Param{{Name: "text", Type: refString}},
			ReturnType: llvmTypes.usize,
			EntryID:    0,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.Assign{Name: "length", Value: &mir.Len{
					Value: &mir.RefName{Name: "text", Type: refString}, Type: llvmTypes.usize,
				}}},
				Term: &mir.Ret{Value: &mir.RefName{Name: "length", Type: llvmTypes.usize}},
			}},
		}},
	}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "extractvalue { i8*, i64 } %text, 1") {
		t.Fatalf("expected borrowed string length field extraction, got:\n%s", irText)
	}
	if strings.Contains(irText, "extractvalue { i8*, i64 } %text, 0") {
		t.Fatalf("borrowed string length must not extract unused data field, got:\n%s", irText)
	}
}

func indexedMIRPlace(baseType ir.TypeID, index mir.ValueRef) *mir.Place {
	return &mir.Place{
		Root: &mir.RefName{Name: "xs", Type: baseType},
		Projections: []mir.PlaceProjection{
			{Kind: mir.PlaceProjectionIndex, Index: index, Type: llvmTypes.i32},
		},
		Type: llvmTypes.i32,
	}
}

func indexReadMIRModule(baseType ir.TypeID, index mir.ValueRef) *mir.Module {
	params := []ir.Param{{Name: "xs", Type: baseType}}
	if ref, ok := index.(*mir.RefName); ok {
		params = append(params, ir.Param{Name: ref.Name, Type: ref.Type})
	}
	return &mir.Module{
		Name:     "test",
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "first",
				Params:     params,
				ReturnType: llvmTypes.i32,
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{
								Name:  "item",
								Value: &mir.Load{Place: indexedMIRPlace(baseType, index), Type: llvmTypes.i32},
							},
						},
						Term: &mir.Ret{Value: &mir.RefName{Name: "item", Type: llvmTypes.i32}},
					},
				},
			},
		},
	}
}

func indexStoreMIRModule(baseType ir.TypeID, index mir.ValueRef) *mir.Module {
	return &mir.Module{
		Name:     "test",
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name: "set_item",
				Params: []ir.Param{
					{Name: "xs", Type: baseType},
					{Name: "value", Type: llvmTypes.i32},
				},
				ReturnType: llvmTypes.i32,
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Store{
								Place: indexedMIRPlace(baseType, index),
								Value: &mir.RefName{Name: "value", Type: llvmTypes.i32},
							},
						},
						Term: &mir.Ret{Value: &mir.RefConst{Value: "0", Type: llvmTypes.i32}},
					},
				},
			},
		},
	}
}

func TestGenerateLLVMIRLowersIndexPlaceForArrayRead(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := indexReadMIRModule(llvmTypes.fixed4I32, &mir.RefConst{Value: "0", Type: llvmTypes.i32})
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "getelementptr inbounds [4 x i32], [4 x i32]*") {
		t.Fatalf("expected array index to lower as GEP, got:\n%s", irText)
	}
	if !strings.Contains(irText, "load i32, i32*") {
		t.Fatalf("expected array index read to load element, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRBoundsChecksRuntimeFixedArrayIndex(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := indexReadMIRModule(llvmTypes.fixed4I32, &mir.RefName{Name: "index", Type: llvmTypes.i32})
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	for _, expected := range []string{"sext i32 %index to i64", "icmp uge i64", "call void @llvm.trap()", "getelementptr inbounds [4 x i32]"} {
		if !strings.Contains(irText, expected) {
			t.Fatalf("expected %q in runtime fixed-array index IR:\n%s", expected, irText)
		}
	}
}

func TestGenerateLLVMIRBoundsChecksWideRuntimeFixedArrayIndexBeforeTruncation(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := indexReadMIRModule(llvmTypes.fixed4I32, &mir.RefName{Name: "index", Type: llvmTypes.u128})
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	for _, expected := range []string{"zext i64 4 to i128", "icmp uge i128 %index", "trunc i128 %index to i64", "getelementptr inbounds [4 x i32]"} {
		if !strings.Contains(irText, expected) {
			t.Fatalf("expected %q in wide fixed-array index IR:\n%s", expected, irText)
		}
	}
}

func TestGenerateLLVMIRLowersIndexPlaceStoreForArrayWrite(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	mod := indexStoreMIRModule(llvmTypes.fixed4I32, &mir.RefConst{Value: "0", Type: llvmTypes.i32})
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
		Types:    llvmTypes.table,
		FilePath: unixTestPath,
		Funcs: []*mir.Function{
			{
				Name:       "make",
				ReturnType: llvmTypes.fixed3I32,
				EntryID:    0,
				Blocks: []*mir.Block{
					{
						ID: 0,
						Instrs: []mir.Instr{
							&mir.Assign{
								Name: "arr",
								Value: &mir.ArrayLit{
									Values: []mir.ValueRef{
										&mir.RefConst{Value: "1", Type: llvmTypes.i32},
										&mir.RefConst{Value: "2", Type: llvmTypes.i32},
										&mir.RefConst{Value: "3", Type: llvmTypes.i32},
									},
									Type: llvmTypes.fixed3I32,
								},
							},
						},
						Term: &mir.Ret{Value: &mir.RefName{Name: "arr", Type: llvmTypes.fixed3I32}},
					},
				},
			},
		},
	}
	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
	irText := GenerateLLVMIR(indexReadMIRModule(llvmTypes.fixed4I32, &mir.RefConst{Value: "4", Type: llvmTypes.i32}), diag, testLinuxAMD64, false)
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
		irText := GenerateLLVMIR(indexReadMIRModule(llvmTypes.fixed4I32, &mir.RefConst{Value: index, Type: llvmTypes.i32}), diag, testLinuxAMD64, false)
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
	irText := GenerateLLVMIR(indexReadMIRModule(llvmTypes.dynamicI32, &mir.RefName{Name: "i", Type: llvmTypes.i32}), diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "extractvalue { i32*, i64, i64, i8* } %xs, 0") {
		t.Fatalf("expected dynamic array index to extract data pointer, got:\n%s", irText)
	}
	if !strings.Contains(irText, "extractvalue { i32*, i64, i64, i8* } %xs, 1") ||
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

func TestGenerateLLVMIRLowersBorrowedDynamicArrayIndexRead(t *testing.T) {
	irText := GenerateLLVMIR(indexReadMIRModule(llvmTypes.refDynamicI32, &mir.RefConst{Value: "0", Type: llvmTypes.i32}), diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "load { i32*, i64, i64, i8* }, { i32*, i64, i64, i8* }* %xs") {
		t.Fatalf("expected borrowed dynamic owner to load header before field extraction, got:\n%s", irText)
	}
	if !strings.Contains(irText, "extractvalue { i32*, i64, i64, i8* }") || !strings.Contains(irText, "getelementptr i32, i32*") {
		t.Fatalf("expected borrowed dynamic owner data extraction and indexing, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRUsesWidenedUnsignedDynamicArrayIndexForGEP(t *testing.T) {
	const targetTriple = "x86_64-unknown-linux-gnu"
	irText := GenerateLLVMIR(indexReadMIRModule(llvmTypes.dynamicI32, &mir.RefName{Name: "i", Type: llvmTypes.u8}), diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
	irText := GenerateLLVMIR(indexStoreMIRModule(llvmTypes.dynamicI32, &mir.RefConst{Value: "0", Type: llvmTypes.i32}), diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "extractvalue { i32*, i64, i64, i8* } %xs, 0") {
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
	irText := GenerateLLVMIR(indexReadMIRModule(llvmTypes.refSliceI32, &mir.RefName{Name: "i", Type: llvmTypes.i32}), diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 0") ||
		!strings.Contains(irText, "extractvalue { i32*, i64 } %xs, 1") {
		t.Fatalf("expected slice-view data and length extraction, got:\n%s", irText)
	}
	if strings.Contains(irText, "extractvalue { i32*, i64, i64, i8* } %xs") {
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
	irText := GenerateLLVMIR(indexStoreMIRModule(llvmTypes.mutRefSliceI32, &mir.RefConst{Value: "0", Type: llvmTypes.u8}), diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
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
	tokenType := llvmTypes.valueStruct
	itemsType := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeArray, Elem: tokenType})
	bucketType := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeStruct, Fields: []ir.TypeField{{Name: "items", Type: itemsType}}})
	borrowedBucket := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeReference, Mutable: true, Elem: bucketType})
	place := &mir.Place{
		Root: &mir.RefName{Name: "bucket", Type: borrowedBucket},
		Projections: []mir.PlaceProjection{
			{Kind: mir.PlaceProjectionDeref, Type: bucketType},
			{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: itemsType},
			{Kind: mir.PlaceProjectionIndex, Index: &mir.RefName{Name: "index", Type: llvmTypes.usize}, Type: tokenType},
			{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: llvmTypes.i32},
		},
		Type: llvmTypes.i32,
	}
	mod := &mir.Module{Name: "test", Types: llvmTypes.table, Funcs: []*mir.Function{{
		Name: "read", Params: []ir.Param{{Name: "bucket", Type: borrowedBucket}, {Name: "index", Type: llvmTypes.usize}},
		ReturnType: llvmTypes.i32, EntryID: 0,
		Blocks: []*mir.Block{{
			ID:     0,
			Instrs: []mir.Instr{&mir.Assign{Name: "value", Value: &mir.Load{Place: place, Type: llvmTypes.i32}}},
			Term:   &mir.Ret{Value: &mir.RefName{Name: "value", Type: llvmTypes.i32}},
		}},
	}}}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	headerField := strings.Index(irText, "getelementptr inbounds { { { i32 }*, i64, i64, i8* } }")
	element := strings.Index(irText, "getelementptr { i32 }, { i32 }*")
	valueField := strings.LastIndex(irText, "getelementptr inbounds { i32 }, { i32 }*")
	if headerField < 0 || element < headerField || valueField < element || !strings.Contains(irText, "load i32, i32*") {
		t.Fatalf("deep place projections must lower in path order, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRAllocatesPlaceRootBeforeBranches(t *testing.T) {
	boxType := llvmTypes.valueStruct
	place := &mir.Place{
		Root: &mir.RefName{Name: "box", Type: boxType},
		Projections: []mir.PlaceProjection{
			{Kind: mir.PlaceProjectionField, FieldIndex: 0, Type: llvmTypes.i32},
		},
		Type: llvmTypes.i32,
	}
	mod := &mir.Module{Name: "test", Types: llvmTypes.table, Funcs: []*mir.Function{{
		Name: "choose", Params: []ir.Param{{Name: "cond", Type: llvmTypes.boolType}}, ReturnType: llvmTypes.i32, EntryID: 0,
		Blocks: []*mir.Block{
			{ID: 0, Instrs: []mir.Instr{&mir.Assign{Name: "box", Value: &mir.StructLit{Fields: []mir.ValueRef{&mir.RefConst{Value: "0", Type: llvmTypes.i32}}, Type: boxType}}}, Term: &mir.Branch{Cond: &mir.RefName{Name: "cond", Type: llvmTypes.boolType}, ThenID: 1, ElseID: 2}},
			{ID: 1, Instrs: []mir.Instr{&mir.Store{Place: place, Value: &mir.RefConst{Value: "1", Type: llvmTypes.i32}}}, Term: &mir.Jump{TargetID: 3}},
			{ID: 2, Instrs: []mir.Instr{&mir.Store{Place: place, Value: &mir.RefConst{Value: "2", Type: llvmTypes.i32}}}, Term: &mir.Jump{TargetID: 3}},
			{ID: 3, Instrs: []mir.Instr{&mir.Assign{Name: "value", Value: &mir.Load{Place: place, Type: llvmTypes.i32}}}, Term: &mir.Ret{Value: &mir.RefName{Name: "value", Type: llvmTypes.i32}}},
		},
	}}}

	irText := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	alloca := strings.Index(irText, "alloca { i32 }")
	firstBranch := strings.Index(irText, "\nb1:")
	if alloca < 0 || firstBranch < 0 || alloca > firstBranch || strings.Count(irText, "alloca { i32 }") != 1 {
		t.Fatalf("place root must have one entry-dominating slot, got:\n%s", irText)
	}
}

func TestGenerateLLVMIRConsumingInterfaceCallReleasesStorage(t *testing.T) {
	iface := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeInterface, Methods: []ir.TypeMethod{{Name: "take", Params: []ir.TypeField{{Name: "self", Type: llvmTypes.valueStruct}}, Return: llvmTypes.void}}})
	interfaceType := llvmTypes.table.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: iface})
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "consume",
			Params:     []ir.Param{{Name: "value", Type: interfaceType}},
			ReturnType: llvmTypes.void,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{&mir.InterfaceCall{
					Base:     &mir.RefName{Name: "value", Type: interfaceType},
					Slot:     0,
					Consumes: true,
					Type:     llvmTypes.void,
				}},
				Term: &mir.Ret{},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, "@peeper_default_alloc") ||
		!strings.Contains(out, "void (i8*, i8*)*") ||
		!strings.Contains(out, "call void %") {
		t.Fatalf("expected consuming dispatch before allocator-backed carrier release, got:\n%s", out)
	}
}

func TestGenerateLLVMIRLowersAlloc(t *testing.T) {
	mod := &mir.Module{
		Name: "test", Types: llvmTypes.table,
		Funcs: []*mir.Function{{
			Name:       "main",
			ReturnType: llvmTypes.ownedI32,
			Blocks: []*mir.Block{{
				ID: 0,
				Instrs: []mir.Instr{
					&mir.Assign{
						Name:  "p",
						Value: &mir.Alloc{Value: &mir.RefConst{Value: "42", Type: llvmTypes.i32}, Type: llvmTypes.ownedI32},
					},
				},
				Term: &mir.Ret{Value: &mir.RefName{Name: "p", Type: llvmTypes.ownedI32}},
			}},
		}},
	}
	out := GenerateLLVMIR(mod, diagnostics.NewDiagnosticBag(), testLinuxAMD64, false)
	if !strings.Contains(out, "@peeper_default_alloc") {
		t.Fatalf("expected default allocator descriptor, got:\n%s", out)
	}
	if !strings.Contains(out, "bitcast [3 x i8*]* @peeper_default_alloc to i8*") {
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
