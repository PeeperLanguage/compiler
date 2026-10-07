package mir

import (
	"strings"
	"testing"

	"compiler/internal/ir"
)

// wellFormed is one function with a branch and a join, the smallest shape that
// exercises every kind of transfer check.
func wellFormed() *Module {
	types := ir.NewTypeTable()
	voidType := types.Intern(ir.Type{Kind: ir.TypeVoid})
	boolType := types.Intern(ir.Type{Kind: ir.TypeBool})
	return &Module{
		Name: "probe", Types: types,
		Funcs: []*Function{{
			Name: "choose", ReturnType: voidType, EntryID: 0,
			Blocks: []*Block{
				{ID: 0, Term: &Branch{Cond: &RefConst{Value: "true", Type: boolType}, ThenID: 1, ElseID: 2}},
				{ID: 1, Term: &Jump{TargetID: 3}},
				{ID: 2, Term: &Jump{TargetID: 3}},
				{ID: 3, Term: &Ret{}},
			},
		}},
	}
}

// The positive case has to exist, or every negative case below could pass
// against a fixture that was already broken.
func TestValidateAcceptsWellFormedModule(t *testing.T) {
	if err := wellFormed().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a well-formed module", err)
	}
}

func TestValidateReportsDefects(t *testing.T) {
	tests := []struct {
		name   string
		damage func(*Module)
		want   string
	}{
		{
			name:   "block without a terminator",
			damage: func(m *Module) { m.Funcs[0].Blocks[1].Term = nil },
			want:   "block b1 has no terminator",
		},
		{
			name:   "jump to a block that does not exist",
			damage: func(m *Module) { m.Funcs[0].Blocks[1].Term = &Jump{TargetID: 99} },
			want:   "transfers to b99 as its jump target",
		},
		{
			name:   "branch with a missing false target",
			damage: func(m *Module) { m.Funcs[0].Blocks[0].Term = &Branch{ThenID: 1, ElseID: 42} },
			want:   "transfers to b42 as its false target",
		},
		{
			name:   "duplicate block identity",
			damage: func(m *Module) { m.Funcs[0].Blocks[2].ID = 1 },
			want:   "declares block b1 twice",
		},
		{
			name:   "entry naming a block the function does not contain",
			damage: func(m *Module) { m.Funcs[0].EntryID = 7 },
			want:   "enters at b7, which it does not contain",
		},
		{
			name:   "nil instruction",
			damage: func(m *Module) { m.Funcs[0].Blocks[0].Instrs = []Instr{nil} },
			want:   "holds a nil instruction at 0",
		},
		{
			name:   "typed-nil terminator",
			damage: func(m *Module) { m.Funcs[0].Blocks[1].Term = (*Jump)(nil) },
			want:   "block b1 has no terminator",
		},
		{
			name:   "typed-nil instruction",
			damage: func(m *Module) { m.Funcs[0].Blocks[0].Instrs = []Instr{(*Store)(nil)} },
			want:   "holds a nil instruction at 0",
		},
		{
			name: "one case selected twice",
			damage: func(m *Module) {
				m.Funcs[0].Blocks[0].Term = &SwitchVariant{Targets: []VariantTarget{
					{Case: 0, TargetID: 1}, {Case: 0, TargetID: 2},
				}}
			},
			want: "selects case 0 twice",
		},
		{
			name:   "module without type table",
			damage: func(m *Module) { m.Types = nil },
			want:   "module with typed MIR artifacts has no type table",
		},
		{
			name: "branch on non-bool value",
			damage: func(m *Module) {
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				m.Funcs[0].Blocks[0].Term = &Branch{Cond: &RefConst{Value: "1", Type: i32}, ThenID: 1, ElseID: 2}
			},
			want: "branches on non-bool",
		},
		{
			name: "store type mismatch",
			damage: func(m *Module) {
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				boolean := m.Types.Intern(ir.Type{Kind: ir.TypeBool})
				m.Funcs[0].Blocks[0].Instrs = []Instr{&Store{
					Place: &Place{Root: &RefName{Name: "slot", Type: i32}, Type: i32},
					Value: &RefConst{Value: "true", Type: boolean},
				}}
			},
			want: "stores type#",
		},
		{
			name: "binary operand type mismatch",
			damage: func(m *Module) {
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				boolean := m.Types.Intern(ir.Type{Kind: ir.TypeBool})
				m.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Name: "bad", Value: &Binary{
					Op: "+", Left: &RefConst{Value: "1", Type: i32}, Right: &RefConst{Value: "true", Type: boolean}, Type: i32,
				}}}
			},
			want: "binary operands have mismatched types",
		},
		{
			name: "call argument type mismatch",
			damage: func(m *Module) {
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				boolean := m.Types.Intern(ir.Type{Kind: ir.TypeBool})
				voidType := m.Funcs[0].ReturnType
				fnType := m.Types.Intern(ir.Type{Kind: ir.TypeFunction, Params: []ir.TypeID{i32}, Return: voidType})
				m.Funcs[0].Blocks[0].Instrs = []Instr{&Call{
					Callee: &RefName{Name: "callee", Type: fnType},
					Args:   []ValueRef{&RefConst{Value: "true", Type: boolean}},
					Type:   voidType,
				}}
			},
			want: "argument 0 has type#",
		},
		{
			name: "dynamic array operation without reference carrier",
			damage: func(m *Module) {
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				array := m.Types.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32})
				m.Funcs[0].Blocks[0].Instrs = []Instr{&DynamicArrayOp{
					Array: &RefName{Name: "values", Type: array}, ArrayType: array,
				}}
			},
			want: "does not reference array type#",
		},
		{
			name: "index projection element mismatch",
			damage: func(m *Module) {
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				boolean := m.Types.Intern(ir.Type{Kind: ir.TypeBool})
				array := m.Types.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32, Length: "1"})
				m.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Name: "value", Value: &Load{
					Place: &Place{
						Root: &RefName{Name: "values", Type: array},
						Projections: []PlaceProjection{{
							Kind: PlaceProjectionIndex, Index: &RefConst{Value: "0", Type: i32}, Type: boolean,
						}},
						Type: boolean,
					},
					Type: boolean,
				}}}
			},
			want: "indexes incompatible type#",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module := wellFormed()
			test.damage(module)
			err := module.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want a report containing %q", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want a report containing %q", err, test.want)
			}
		})
	}
}

func typedNilValueRef() ValueRef {
	var ref *RefName
	return ref
}

func typedNilValueExpr() ValueExpr {
	var expr *Unary
	return expr
}

func assertValidationError(t *testing.T, module *Module, want string) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("Validate() panicked: %v", recovered)
		}
	}()
	err := module.Validate()
	if err == nil {
		t.Fatalf("Validate() = nil, want a report containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Validate() = %v, want a report containing %q", err, want)
	}
}

func TestValidateRejectsNestedTypedNilValues(t *testing.T) {
	tests := []struct {
		name   string
		module func() *Module
		want   string
	}{
		{
			name: "assignment expression",
			module: func() *Module {
				module := wellFormed()
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{
					Name: "broken", Value: typedNilValueExpr(),
				}}
				return module
			},
			want: "assignment is nil",
		},
		{
			name: "unary operand",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{
					Name: "broken", Value: &Unary{Arg: typedNilValueRef(), Type: i32},
				}}
				return module
			},
			want: "assignment operand is nil",
		},
		{
			name: "store value",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Store{
					Place: &Place{Root: &RefName{Name: "slot", Type: i32}, Type: i32},
					Value: typedNilValueRef(),
				}}
				return module
			},
			want: "store value is nil",
		},
		{
			name: "dynamic array value",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				array := module.Types.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&DynamicArrayOp{Array: typedNilValueRef(), ArrayType: array}}
				return module
			},
			want: "dynamic-array value is nil",
		},
		{
			name: "branch condition",
			module: func() *Module {
				module := wellFormed()
				module.Funcs[0].Blocks[0].Term = &Branch{Cond: typedNilValueRef(), ThenID: 1, ElseID: 2}
				return module
			},
			want: "branch condition is nil",
		},
		{
			name: "variant switch value",
			module: func() *Module {
				module := wellFormed()
				module.Funcs[0].Blocks[0].Term = &SwitchVariant{Value: typedNilValueRef()}
				return module
			},
			want: "variant switch value is nil",
		},
		{
			name: "return value",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				module.Funcs[0].ReturnType = i32
				module.Funcs[0].Blocks[3].Term = &Ret{Value: typedNilValueRef()}
				return module
			},
			want: "return value is nil",
		},
		{
			name: "field base",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &Field{Base: typedNilValueRef(), Index: 0, Type: i32}}}
				return module
			},
			want: "field base is nil",
		},
		{
			name: "allocation value",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				owned := module.Types.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: i32})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &Alloc{Value: typedNilValueRef(), Type: owned}}}
				return module
			},
			want: "allocated value is nil",
		},
		{
			name: "call callee",
			module: func() *Module {
				module := wellFormed()
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &Call{Callee: typedNilValueRef(), Type: module.Funcs[0].ReturnType}}}
				return module
			},
			want: "callee is nil",
		},
		{
			name: "call argument",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				fnType := module.Types.Intern(ir.Type{Kind: ir.TypeFunction, Params: []ir.TypeID{i32}, Return: module.Funcs[0].ReturnType})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &Call{
					Callee: &RefName{Name: "callee", Type: fnType}, Args: []ValueRef{typedNilValueRef()}, Type: module.Funcs[0].ReturnType,
				}}}
				return module
			},
			want: "argument 0 is nil",
		},
		{
			name: "interface call base",
			module: func() *Module {
				module, _, _, _ := interfaceValidationFixture()
				call := module.Funcs[0].Blocks[0].Instrs[0].(*InterfaceCall)
				call.Base = typedNilValueRef()
				return module
			},
			want: "interface base is nil",
		},
		{
			name: "interface call argument",
			module: func() *Module {
				module, _, slotType, _ := interfaceValidationFixture()
				call := module.Funcs[0].Blocks[0].Instrs[0].(*InterfaceCall)
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				slot, _ := module.Types.Type(slotType)
				call.SlotType = module.Types.Intern(ir.Type{Kind: ir.TypeFunction, Params: []ir.TypeID{slot.Params[0], i32}, Return: slot.Return})
				call.Args = []ValueRef{typedNilValueRef()}
				return module
			},
			want: "interface argument 0 is nil",
		},
		{
			name: "interface construction slot",
			module: func() *Module {
				module, carrier, _, _ := interfaceValidationFixture()
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &InterfaceMake{
					Value: &RefName{Name: "value", Type: carrier}, DataType: carrier,
					Slots: []ValueRef{typedNilValueRef()}, Type: carrier,
				}}}
				return module
			},
			want: "interface slot 0 is nil",
		},
		{
			name: "struct literal field",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				structure := module.Types.Intern(ir.Type{Kind: ir.TypeStruct, Fields: []ir.TypeField{{Name: "value", Type: i32}}})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &StructLit{Fields: []ValueRef{typedNilValueRef()}, Type: structure}}}
				return module
			},
			want: "field 0 is nil",
		},
		{
			name: "array literal element",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				array := module.Types.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &ArrayLit{Values: []ValueRef{typedNilValueRef()}, Type: array}}}
				return module
			},
			want: "element 0 is nil",
		},
		{
			name: "variant payload",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				variant := module.Types.Intern(ir.Type{Kind: ir.TypeVariant, Cases: []ir.VariantCase{{Name: "Some", Payload: i32}}})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &VariantMake{Case: 0, Payload: typedNilValueRef(), Type: variant}}}
				return module
			},
			want: "variant payload is nil",
		},
		{
			name: "payloadless variant payload",
			module: func() *Module {
				module := wellFormed()
				variant := module.Types.Intern(ir.Type{Kind: ir.TypeVariant, Cases: []ir.VariantCase{{Name: "None"}}})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &VariantMake{Case: 0, Payload: typedNilValueRef(), Type: variant}}}
				return module
			},
			want: "variant payload is nil",
		},
		{
			name: "variant test value",
			module: func() *Module {
				module := wellFormed()
				boolean := module.Types.Intern(ir.Type{Kind: ir.TypeBool})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &VariantIs{Value: typedNilValueRef(), Case: 0, Type: boolean}}}
				return module
			},
			want: "variant value is nil",
		},
		{
			name: "place root",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &Load{Place: &Place{Root: typedNilValueRef(), Type: i32}, Type: i32}}}
				return module
			},
			want: "load place root is nil",
		},
		{
			name: "place index",
			module: func() *Module {
				module := wellFormed()
				i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
				array := module.Types.Intern(ir.Type{Kind: ir.TypeArray, Elem: i32, Length: "1"})
				module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &Load{Place: &Place{
					Root: &RefName{Name: "values", Type: array}, Projections: []PlaceProjection{{
						Kind: PlaceProjectionIndex, Index: typedNilValueRef(), Type: i32,
					}}, Type: i32,
				}, Type: i32}}}
				return module
			},
			want: "projection 0 index is nil",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertValidationError(t, test.module(), test.want)
		})
	}
}

func TestValidatePreservesPlainNilSemantics(t *testing.T) {
	t.Run("missing return value remains diagnostic", func(t *testing.T) {
		module := wellFormed()
		i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
		module.Funcs[0].ReturnType = i32
		module.Funcs[0].Blocks[3].Term = &Ret{}
		assertValidationError(t, module, "returns no value")
	})

	t.Run("payloadless variant may omit payload", func(t *testing.T) {
		module := wellFormed()
		variant := module.Types.Intern(ir.Type{Kind: ir.TypeVariant, Cases: []ir.VariantCase{{Name: "None"}}})
		module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Value: &VariantMake{Case: 0, Type: variant}}}
		if err := module.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil for payloadless variant", err)
		}
	})
}

func TestValidateAcceptsMixedWidthShiftCount(t *testing.T) {
	module := wellFormed()
	i32 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
	i64 := module.Types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 64})
	module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Name: "shifted", Value: &Binary{
		Op: "<<", Left: &RefConst{Value: "1", Type: i32}, Right: &RefConst{Value: "2", Type: i64}, Type: i32,
	}}}
	if err := module.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want mixed-width shift count accepted", err)
	}
}

// An unsorted report differs between runs of the same broken artifact, which is
// useless to whoever has to fix it.
func TestValidateReportsDefectsDeterministically(t *testing.T) {
	module := wellFormed()
	module.Funcs[0].Blocks[1].Term = nil
	module.Funcs[0].Blocks[2].Term = nil
	first := module.Validate()
	for range 8 {
		if got := module.Validate(); got.Error() != first.Error() {
			t.Fatalf("Validate() = %v, want the stable report %v", got, first)
		}
	}
}

func TestValidateChecksModuleArtifactsWithoutFunctions(t *testing.T) {
	types := ir.NewTypeTable()
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
	module := &Module{Types: types, InterfaceThunks: []*InterfaceThunk{{
		Name: "broken", SlotType: ir.TypeID(999), FuncType: i32, DataType: i32,
	}}}
	if err := module.Validate(); err == nil || !strings.Contains(err.Error(), "invalid slot type#999") {
		t.Fatalf("Validate() = %v, want invalid interface thunk slot type", err)
	}
}

func TestValidateRejectsTypedArtifactsWithoutTypeTable(t *testing.T) {
	module := &Module{InterfaceThunks: []*InterfaceThunk{{Name: "broken"}}}
	if err := module.Validate(); err == nil || !strings.Contains(err.Error(), "typed MIR artifacts has no type table") {
		t.Fatalf("Validate() = %v, want missing type table", err)
	}
}

func TestValidateRejectsNilStaticEntryWithoutFunctions(t *testing.T) {
	module := &Module{StaticData: []*StaticEntry{nil}}
	if err := module.Validate(); err == nil || !strings.Contains(err.Error(), "nil static entry at 0") {
		t.Fatalf("Validate() = %v, want nil static entry", err)
	}
}

func TestValidateAcceptsEmptyModule(t *testing.T) {
	if err := (*Module)(nil).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for an empty artifact", err)
	}
	if err := (&Module{}).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a module with no functions", err)
	}
}

func interfaceValidationFixture() (*Module, ir.TypeID, ir.TypeID, ir.TypeID) {
	types := ir.NewTypeTable()
	voidType := types.Intern(ir.Type{Kind: ir.TypeVoid})
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, IsSigned: true, Bits: 32})
	rawptr := types.Intern(ir.Type{Kind: ir.TypeRawPtr})
	slotType := types.Intern(ir.Type{Kind: ir.TypeFunction, Params: []ir.TypeID{rawptr}, Return: voidType})
	wrongSlotType := types.Intern(ir.Type{Kind: ir.TypeFunction, Params: []ir.TypeID{rawptr}, Return: i32})
	iface := types.Intern(ir.Type{Kind: ir.TypeInterface, Methods: []ir.TypeMethod{{
		Name: "take", Receiver: ir.MethodReceiverValue, Return: voidType, SlotType: slotType,
	}}})
	carrier := types.Intern(ir.Type{Kind: ir.TypeOwnedPtr, Elem: iface})
	module := &Module{
		Name: "interface_validation", Types: types,
		Funcs: []*Function{{
			Name: "consume", Params: []ir.Param{{Name: "value", Type: carrier}}, ReturnType: voidType,
			Blocks: []*Block{{ID: 0, Instrs: []Instr{&InterfaceCall{
				Base: &RefName{Name: "value", Type: carrier}, Slot: 0, SlotType: slotType, Type: voidType,
			}}, Term: &Ret{}}},
		}},
	}
	return module, carrier, slotType, wrongSlotType
}

func TestValidateRejectsInterfaceCallSlotIndexMismatch(t *testing.T) {
	module, _, _, _ := interfaceValidationFixture()
	call := module.Funcs[0].Blocks[0].Instrs[0].(*InterfaceCall)
	call.Slot = 1
	if err := module.Validate(); err == nil || !strings.Contains(err.Error(), "targets invalid interface slot 1") {
		t.Fatalf("Validate() = %v, want invalid interface slot", err)
	}
}

func TestValidateRejectsInterfaceCallSlotTypeMismatch(t *testing.T) {
	module, _, _, wrongSlotType := interfaceValidationFixture()
	call := module.Funcs[0].Blocks[0].Instrs[0].(*InterfaceCall)
	call.SlotType = wrongSlotType
	if err := module.Validate(); err == nil || !strings.Contains(err.Error(), "want published type#") {
		t.Fatalf("Validate() = %v, want published interface slot type mismatch", err)
	}
}

func TestValidateRejectsInterfaceConstructionSlotTypeMismatch(t *testing.T) {
	module, carrier, _, wrongSlotType := interfaceValidationFixture()
	module.Funcs[0].Blocks[0].Instrs = []Instr{&Assign{Name: "erased", Value: &InterfaceMake{
		Value: &RefName{Name: "value", Type: carrier}, DataType: carrier,
		Slots: []ValueRef{&RefName{Name: "thunk", Type: wrongSlotType}}, Type: carrier,
	}}}
	if err := module.Validate(); err == nil || !strings.Contains(err.Error(), "want published type#") {
		t.Fatalf("Validate() = %v, want published construction slot type mismatch", err)
	}
}
