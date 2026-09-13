package hir

import (
	"strings"
	"testing"

	"compiler/internal/ir"
)

func wellFormedHIR() *Module {
	return &Module{
		Name: "probe",
		Funcs: []*Function{{
			Name:       "choose",
			ReturnType: 1,
			Body: &Block{Stmts: []Stmt{
				&If{Cond: &ir.BoolLit{Type: 1}, Then: &Block{}, Else: &Block{}},
				&For{Body: &Block{}},
				&SwitchVariant{Value: &ir.Ident{Name: "value", Type: 1}, Cases: []VariantCaseBlock{{Case: 0, Body: &Block{}}}},
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

// A typed nil wraps a nil pointer in a non-nil interface, so it must be treated
// like a plain nil optional slot rather than dereferenced as a real statement.
func TestValidateAcceptsTypedNilOptionalSlot(t *testing.T) {
	module := wellFormedHIR()
	module.Funcs[0].Body.Stmts[0].(*If).Else = (*Block)(nil)
	if err := module.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a typed-nil optional else", err)
	}
}

func TestValidateReportsDefects(t *testing.T) {
	tests := []struct {
		name   string
		damage func(*Module)
		want   string
	}{
		{
			name:   "function with invalid return type",
			damage: func(m *Module) { m.Funcs[0].ReturnType = ir.InvalidType },
			want:   "function choose has invalid return type",
		},
		{
			name:   "function with invalid parameter type",
			damage: func(m *Module) { m.Funcs[0].Params = []ir.Param{{Name: "value"}} },
			want:   "function choose has parameter 0 with invalid type",
		},
		{
			name: "extern with invalid signature",
			damage: func(m *Module) {
				m.Externs = []Extern{{Name: "read", Params: []ir.Param{{Name: "value"}}, ReturnType: 1}}
			},
			want: "extern read has parameter 0 with invalid type",
		},
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
			name: "assignment with invalid projection type",
			damage: func(m *Module) {
				m.Funcs[0].Body.Stmts = append(m.Funcs[0].Body.Stmts, &Assign{
					Target: &ir.Place{
						Root: &ir.Ident{Name: "value", Type: 1}, Type: 1,
						Projections: []ir.PlaceProjection{{Kind: ir.PlaceProjectionField}},
					},
					Value: &ir.BoolLit{Type: 1},
				})
			},
			want: "assignment target projection 0 with invalid type",
		},
		{
			name: "assignment with unknown projection kind",
			damage: func(m *Module) {
				m.Funcs[0].Body.Stmts = append(m.Funcs[0].Body.Stmts, &Assign{
					Target: &ir.Place{
						Root: &ir.Ident{Name: "value", Type: 1}, Type: 1,
						Projections: []ir.PlaceProjection{{Kind: ir.PlaceProjectionKind(255), Type: 1}},
					},
					Value: &ir.BoolLit{Type: 1},
				})
			},
			want: "assignment target projection 0 with unknown kind 255",
		},
		{
			name: "variant binding with invalid type",
			damage: func(m *Module) {
				arm := &m.Funcs[0].Body.Stmts[2].(*SwitchVariant).Cases[0]
				arm.PayloadType = 1
				arm.Bindings = []VariantBinding{{Name: "payload"}}
			},
			want: "case 0 has binding 0 with invalid type",
		},
		{
			name: "invalid nested expression",
			damage: func(m *Module) {
				m.Funcs[0].Body.Stmts[0].(*If).Cond = &ir.Binary{
					Left: &ir.InvalidExpr{Message: "missing operand evidence"}, Right: &ir.BoolLit{Type: 1}, Type: 1,
				}
			},
			want: "function choose has invalid if condition: missing operand evidence",
		},
		{
			name:   "invalid expression type",
			damage: func(m *Module) { m.Funcs[0].Body.Stmts[0].(*If).Cond = &ir.BoolLit{} },
			want:   "function choose has if condition with invalid type",
		},
		{
			name:   "explicit invalid statement",
			damage: func(m *Module) { m.Funcs[0].Body.Stmts[3] = &Invalid{Message: "missing evidence"} },
			want:   "function choose contains invalid statement: missing evidence",
		},
		{
			name:   "typed-nil block statement in a block",
			damage: func(m *Module) { m.Funcs[0].Body.Stmts[3] = (*Block)(nil) },
			want:   "holds a nil statement at block index 3",
		},
		{
			name:   "typed-nil return statement in a block",
			damage: func(m *Module) { m.Funcs[0].Body.Stmts[3] = (*Return)(nil) },
			want:   "holds a nil statement at block index 3",
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
