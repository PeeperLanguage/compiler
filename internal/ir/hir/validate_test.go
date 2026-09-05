package hir

import (
	"strings"
	"testing"
)

func wellFormedHIR() *Module {
	return &Module{
		Name: "probe",
		Funcs: []*Function{{
			Name: "choose",
			Body: &Block{Stmts: []Stmt{
				&If{Then: &Block{}, Else: &Block{}},
				&For{Body: &Block{}},
				&SwitchVariant{Cases: []VariantCaseBlock{{Case: 0, Body: &Block{}}}},
				&Return{},
			}},
		}},
	}
}

// Without a positive case, a negative one could pass against a fixture that was
// already broken.
func TestValidateAcceptsWellFormedModule(t *testing.T) {
	if err := wellFormedHIR().Validate(); err != nil {
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
			name:   "function with no body",
			damage: func(m *Module) { m.Funcs[0].Body = nil },
			want:   "function choose has no body",
		},
		{
			name:   "nil statement in a block",
			damage: func(m *Module) { m.Funcs[0].Body.Stmts[3] = nil },
			want:   "holds a nil statement at block index 3",
		},
		{
			name:   "if with no then block",
			damage: func(m *Module) { m.Funcs[0].Body.Stmts[0].(*If).Then = nil },
			want:   "has a if with no body",
		},
		{
			name:   "loop with no body",
			damage: func(m *Module) { m.Funcs[0].Body.Stmts[1].(*For).Body = nil },
			want:   "has a loop with no body",
		},
		{
			name: "case arm with no body",
			damage: func(m *Module) {
				m.Funcs[0].Body.Stmts[2].(*SwitchVariant).Cases[0].Body = nil
			},
			want: "has a case 0 with no body",
		},
		{
			name:   "nil function",
			damage: func(m *Module) { m.Funcs = append(m.Funcs, nil) },
			want:   "module holds a nil function",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module := wellFormedHIR()
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

// A nested body is reached through the same walk, so a defect inside one is
// reported rather than skipped.
func TestValidateDescendsIntoNestedBodies(t *testing.T) {
	module := wellFormedHIR()
	module.Funcs[0].Body.Stmts[1].(*For).Body.Stmts = []Stmt{&If{Then: nil}}
	err := module.Validate()
	if err == nil || !strings.Contains(err.Error(), "has a if with no body") {
		t.Fatalf("Validate() = %v, want the nested defect reported", err)
	}
}

func TestValidateAcceptsEmptyModule(t *testing.T) {
	if err := (*Module)(nil).Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for an empty artifact", err)
	}
}
