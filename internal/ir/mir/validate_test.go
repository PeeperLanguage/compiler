package mir

import (
	"strings"
	"testing"
)

// wellFormed is one function with a branch and a join, the smallest shape that
// exercises every kind of transfer check.
func wellFormed() *Module {
	return &Module{
		Name: "probe",
		Funcs: []*Function{{
			Name:    "choose",
			EntryID: 0,
			Blocks: []*Block{
				{ID: 0, Term: &Branch{ThenID: 1, ElseID: 2}},
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

func TestValidateAcceptsEmptyModule(t *testing.T) {
	if err := (*Module)(nil).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for an empty artifact", err)
	}
	if err := (&Module{}).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a module with no functions", err)
	}
}
