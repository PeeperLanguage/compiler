package effect_test

import (
	"strings"
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/symbols"
)

const validationSource = `fn bump(start: i32) -> i32 {
	let mut count = start;
	count = count + 1;
	return count;
}`

// A real artifact from the real producer must validate. Without this, every
// negative case below could pass against a fixture that was already broken.
func TestValidateAcceptsPublishedEffects(t *testing.T) {
	result, module := buildEffects(t, validationSource)
	if err := result.Validate(module.CFG, module.THIR); err != nil {
		t.Fatalf("Validate() = %v, want nil for a published artifact", err)
	}
}

func TestValidateRejectsMissingFunctionEffects(t *testing.T) {
	result, module := buildEffects(t, `fn first() {}
fn second() {}`)
	if len(module.CFG.Functions) != 2 {
		t.Fatalf("CFG functions = %d, want 2", len(module.CFG.Functions))
	}
	delete(result, module.CFG.Functions[1].NodeID)
	if err := result.Validate(module.CFG, module.THIR); err == nil || !strings.Contains(err.Error(), "no published effects") {
		t.Fatalf("Validate() = %v, want missing-function evidence error", err)
	}
}

func TestValidateReportsDefects(t *testing.T) {
	tests := []struct {
		name   string
		damage func(effect.Result, ir.NodeID, cfg.SiteID)
		want   string
	}{
		{
			name: "place naming no root at all",
			damage: func(result effect.Result, fn ir.NodeID, site cfg.SiteID) {
				result[fn][site] = []effect.Op{effect.Use{Node: 1}}
			},
			want: "names neither a binding nor a temporary",
		},
		{
			name: "place naming two roots",
			damage: func(result effect.Result, fn ir.NodeID, site cfg.SiteID) {
				result[fn][site] = []effect.Op{effect.Use{
					Place: effect.Place{Root: &symbols.Symbol{Name: "x"}, Temporary: 1},
					Node:  1,
				}}
			},
			want: "names both binding x and temporary",
		},
		{
			name: "use with no source location",
			damage: func(result effect.Result, fn ir.NodeID, site cfg.SiteID) {
				result[fn][site] = []effect.Op{effect.Use{Place: effect.Place{Root: &symbols.Symbol{Name: "x"}}, Node: 1}}
			},
			want: "is a use with no source location to report against",
		},
		{
			name: "operation naming an unknown node",
			damage: func(result effect.Result, fn ir.NodeID, site cfg.SiteID) {
				result[fn][site] = []effect.Op{effect.Write{Place: effect.Place{Root: &symbols.Symbol{Name: "x"}}, Node: 999999}}
			},
			want: "which is not in the typed THIR",
		},
		{
			name: "effects at a site the graph does not contain",
			damage: func(result effect.Result, fn ir.NodeID, site cfg.SiteID) {
				result[fn][cfg.SiteID{Block: 4242, Index: 7}] = []effect.Op{}
			},
			want: "which the graph does not contain",
		},
		{
			name: "effects for a function with no graph",
			damage: func(result effect.Result, fn ir.NodeID, site cfg.SiteID) {
				result[ir.NodeID(987654)] = effect.SiteOps{}
			},
			want: "has published effects but no control-flow graph",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, module := buildEffects(t, validationSource)
			fn, site := anySite(t, result)
			test.damage(result, fn, site)
			err := result.Validate(module.CFG, module.THIR)
			if err == nil {
				t.Fatalf("Validate() = nil, want a report containing %q", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want a report containing %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsDamagedTHIREvidence(t *testing.T) {
	result, module := buildEffects(t, validationSource)
	fn, site := anySite(t, result)
	function := module.THIR.Function(ir.NodeID(fn))
	binding := function.Body.Stmts[0].(*thir.Binding)
	assignment := function.Body.Stmts[1].(*thir.Assign)
	target := assignment.Target
	root := effect.Place{Root: binding.Symbol}
	bindingID := ir.NodeID(binding.Source.NodeID)
	assignID := ir.NodeID(assignment.Source.NodeID)
	targetID := ir.NodeID(target.SourceInfo().NodeID)
	for _, test := range []struct {
		name string
		ops  []effect.Op
		want string
	}{
		{"define source", []effect.Op{effect.Define{Symbol: binding.Symbol, Source: binding, Node: assignID}}, "does not match node"},
		{"define value", []effect.Op{effect.Define{Symbol: binding.Symbol, Source: binding, Node: bindingID, Value: assignID, ValueExpr: binding.Value}}, "unexpected node type"},
		{"parameter identity", []effect.Op{effect.Define{Symbol: binding.Symbol, Node: ir.NodeID(function.Params[0].Source.NodeID), IsOnEntry: true}}, "not in typed THIR"},
		{"write target", []effect.Op{effect.Write{Place: root, Node: assignID, Target: target, Owner: assignID}}, "unexpected node type"},
		{"write owner", []effect.Op{effect.Write{Place: root, Node: targetID, Target: target, Owner: bindingID}}, "unexpected node type"},
		{"write value", []effect.Op{effect.Write{Place: root, Node: targetID, Target: target, Owner: assignID, Value: assignID, ValueExpr: assignment.Value}}, "unexpected node type"},
		{"use", []effect.Op{effect.Use{Place: root, Node: assignID, Source: target, Location: target.SourceInfo().Location}}, "unexpected node type"},
		{"missing use source", []effect.Op{effect.Use{Place: root, Node: targetID, Location: target.SourceInfo().Location}}, "does not match node"},
		{"borrow operand", []effect.Op{effect.Borrow{Place: root, Source: target, Node: targetID, Operand: assignID, OperandExpr: target, Location: target.SourceInfo().Location}}, "unexpected node type"},
		{"iteration owner", []effect.Op{effect.Iterate{Place: root, Source: target, Node: targetID, Loop: bindingID, Carrier: binding.Symbol, Location: target.SourceInfo().Location}}, "unexpected node type"},
		{"discard", []effect.Op{effect.Discard{Place: root, Node: assignID, Source: target, Location: target.SourceInfo().Location}}, "unexpected node type"},
		{"call start", []effect.Op{effect.CallBegin{Node: targetID, Source: target}, effect.CallEnd{Node: targetID}}, "unexpected node type"},
		{"temporary", []effect.Op{effect.Use{Place: effect.Place{Temporary: assignID, TemporaryExpr: target}, Node: targetID, Source: target, Location: target.SourceInfo().Location}}, "unexpected node type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			published := effect.Result{fn: effect.SiteOps{site: test.ops}}
			if err := published.Validate(module.CFG, module.THIR); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsMissingTHIR(t *testing.T) {
	result, module := buildEffects(t, validationSource)
	if err := result.Validate(module.CFG, nil); err == nil || !strings.Contains(err.Error(), "typed THIR is missing") {
		t.Fatalf("Validate() = %v, want missing THIR", err)
	}
}

func TestValidateAcceptsEmptyResult(t *testing.T) {
	if err := effect.Result(nil).Validate(nil, nil); err != nil {
		t.Fatalf("Validate() = %v, want nil for an empty artifact", err)
	}
}

// anySite returns one published site so a damage case has somewhere to write.
func anySite(t *testing.T, result effect.Result) (ir.NodeID, cfg.SiteID) {
	t.Helper()
	for fn, siteOps := range result {
		for site := range siteOps {
			return fn, site
		}
	}
	t.Fatal("published result has no sites")
	return 0, cfg.SiteID{}
}
