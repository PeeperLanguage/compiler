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
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
				m.Funcs[0].Blocks[0].Term = &Branch{Cond: &RefConst{Value: "1", Type: i32}, ThenID: 1, ElseID: 2}
			},
			want: "branches on non-bool",
		},
		{
			name: "store type mismatch",
			damage: func(m *Module) {
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
				boolean := m.Types.Intern(ir.Type{Kind: ir.TypeBool})
				m.Funcs[0].Blocks[0].Instrs = []Instr{&Store{
					Place: &Place{Root: &RefName{Name: "slot", Type: i32}, Type: i32},
					Value: &RefConst{Value: "true", Type: boolean},
				}}
			},
			want: "stores type#",
		},
		{
			name: "call argument type mismatch",
			damage: func(m *Module) {
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
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
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
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
				i32 := m.Types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
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
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
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
	i32 := types.Intern(ir.Type{Kind: ir.TypeInteger, Signed: true, Bits: 32})
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
