package ownershipresult

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/bindingresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

// buildGraph produces real CFG topology so the validator is checked against the
// artifact it actually receives, not a hand-shaped stand-in.
func buildGraph(t *testing.T) (*cfg.Module, ir.NodeID) {
	t.Helper()
	const file = "validate_test" + ".peep"
	diag := diagnostics.NewDiagnosticBag()
	source := parser.New(file, lexer.New(file, "fn main() -> i32 {\n\treturn 0;\n}\n", diag).Tokenize(), diag).ParseModule()
	graphs := cfg.BuildModule(source, cfg.BuildQueries{})
	if graphs == nil || len(graphs.Functions) == 0 {
		t.Fatalf("no CFG built: %s", diag.EmitAllToString())
	}
	return graphs, graphs.Functions[0].NodeID
}

func emptyPlan() *CleanupPlan {
	return &CleanupPlan{
		AfterScope:             make(map[cfg.SiteID][]symbols.SymbolID),
		BeforeReturn:           make(map[ir.NodeID][]symbols.SymbolID),
		BeforeAssign:           make(map[ir.NodeID]struct{}),
		DiscardedValue:         make(map[ir.NodeID]struct{}),
		ProjectionBase:         make(map[ir.NodeID]struct{}),
		MatchFieldDrops:        make(map[ir.NodeID][]int),
		MatchWholePayloadDrops: make(map[ir.NodeID]struct{}),
	}
}

func TestValidateAcceptsConsistentEvidence(t *testing.T) {
	graphs, fnID := buildGraph(t)
	result := Result{fnID: emptyPlan()}
	if err := result.Validate(typecheckresult.New(), bindingresult.New(), graphs); err != nil {
		t.Fatalf("consistent evidence rejected: %v", err)
	}
}

func TestValidateRejectsEvidenceGaps(t *testing.T) {
	graphs, fnID := buildGraph(t)
	argument := &ast.Ident{Name: "value"}
	argument.SetID(41)

	for _, tt := range []struct {
		name  string
		want  string
		build func(*typecheckresult.Result, *CleanupPlan)
	}{
		{
			name: "use kind without a type",
			want: "no expression type",
			build: func(types *typecheckresult.Result, _ *CleanupPlan) {
				types.ValueUses[7] = typeinfo.UseMove
			},
		},
		{
			name: "copy of a type with no copy operation",
			want: "no copy operation",
			build: func(types *typecheckresult.Result, _ *CleanupPlan) {
				types.ExprTypes[7] = &typeinfo.StringType{}
				types.ValueUses[7] = typeinfo.UseCopy
			},
		},
		{
			name: "call argument with no use kind",
			want: "no published use kind",
			build: func(types *typecheckresult.Result, _ *CleanupPlan) {
				types.EffectiveCallArguments[5] = []ast.Expr{argument}
			},
		},
		{
			name: "drop at a site that is not a scope exit",
			want: "not a scope exit",
			build: func(_ *typecheckresult.Result, plan *CleanupPlan) {
				plan.AfterScope[cfg.SiteID{}] = []symbols.SymbolID{1}
			},
		},
		{
			name: "return drop at an unknown node",
			want: "not a site in its CFG",
			build: func(_ *typecheckresult.Result, plan *CleanupPlan) {
				plan.BeforeReturn[9999] = []symbols.SymbolID{1}
			},
		},
		{
			name: "unidentified drop target",
			want: "unidentified",
			build: func(_ *typecheckresult.Result, plan *CleanupPlan) {
				plan.BeforeReturn[9999] = []symbols.SymbolID{0}
			},
		},
		{
			name: "projection base with no type",
			want: "no expression type",
			build: func(_ *typecheckresult.Result, plan *CleanupPlan) {
				plan.ProjectionBase[8888] = struct{}{}
			},
		},
		{
			name: "match drop outside a block",
			want: "not a block",
			build: func(_ *typecheckresult.Result, plan *CleanupPlan) {
				plan.MatchWholePayloadDrops[7777] = struct{}{}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			types := typecheckresult.New()
			plan := emptyPlan()
			tt.build(types, plan)
			err := Result{fnID: plan}.Validate(types, bindingresult.New(), graphs)
			if err == nil {
				t.Fatal("inconsistent evidence accepted")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestValidateRejectsPlanWithoutCFG(t *testing.T) {
	graphs, fnID := buildGraph(t)
	err := Result{fnID + 1000: emptyPlan()}.Validate(typecheckresult.New(), bindingresult.New(), graphs)
	if err == nil || !strings.Contains(err.Error(), "no CFG") {
		t.Fatalf("error = %v, want a missing-CFG report", err)
	}
}

func TestValidateRejectsNilPlan(t *testing.T) {
	graphs, fnID := buildGraph(t)
	err := Result{fnID: nil}.Validate(typecheckresult.New(), bindingresult.New(), graphs)
	if err == nil || !strings.Contains(err.Error(), "nil cleanup plan") {
		t.Fatalf("error = %v, want a nil-plan report", err)
	}
}

// Plans and evidence are maps, so an unsorted report would name different
// problems on different runs for one broken module.
func TestValidateReportsProblemsDeterministically(t *testing.T) {
	graphs, fnID := buildGraph(t)
	first := ""
	for attempt := 0; attempt < 8; attempt++ {
		types := typecheckresult.New()
		plan := emptyPlan()
		for id := ast.NodeID(1); id <= 40; id++ {
			types.ValueUses[id] = typeinfo.UseMove
			plan.ProjectionBase[ir.NodeID(id)] = struct{}{}
		}
		err := Result{fnID: plan}.Validate(types, bindingresult.New(), graphs)
		if err == nil {
			t.Fatal("inconsistent evidence accepted")
		}
		if attempt == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("report changed between runs:\n%s\n%s", first, err.Error())
		}
	}
	if !strings.Contains(first, "more)") {
		t.Fatalf("report = %q, want a truncated sample", first)
	}
}
