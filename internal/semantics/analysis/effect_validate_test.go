package analysis_test

import (
	"compiler/internal/source"
	"strings"
	"testing"

	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/analysis"
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
	result, module := buildEffectsForTest(t, validationSource)
	if err := analysis.ValidateEffectsForTest(result, module.CFG, module.THIR); err != nil {
		t.Fatalf("Validate() = %v, want nil for a published artifact", err)
	}
}

func TestValidateRejectsMissingFunctionEffects(t *testing.T) {
	result, module := buildEffectsForTest(t, `fn first() {}
fn second() {}`)
	if len(module.CFG.Functions) != 2 {
		t.Fatalf("CFG functions = %d, want 2", len(module.CFG.Functions))
	}
	delete(result, module.CFG.Functions[1].FunctionID)
	if err := analysis.ValidateEffectsForTest(result, module.CFG, module.THIR); err == nil || !strings.Contains(err.Error(), "no derived effects") {
		t.Fatalf("Validate() = %v, want missing-function evidence error", err)
	}
}

func TestValidateReportsDefects(t *testing.T) {
	tests := []struct {
		name   string
		damage func(analysis.EffectStreamsForTest, moduleid.FunctionID, cfg.SiteID)
		want   string
	}{
		{
			name: "place naming no root at all",
			damage: func(result analysis.EffectStreamsForTest, fn moduleid.FunctionID, site cfg.SiteID) {
				result[fn][site] = []analysis.OpForTest{analysis.UseForTest{Node: source.ParsedNodeID(1)}}
			},
			want: "names neither a binding nor a temporary",
		},
		{
			name: "place naming two roots",
			damage: func(result analysis.EffectStreamsForTest, fn moduleid.FunctionID, site cfg.SiteID) {
				result[fn][site] = []analysis.OpForTest{analysis.UseForTest{
					Place: analysis.PlaceForTest{Root: &symbols.Symbol{Name: "x"}, Temporary: source.ParsedNodeID(1)},
					Node:  source.ParsedNodeID(1),
				}}
			},
			want: "names both binding x and temporary",
		},
		{
			name: "use with no source location",
			damage: func(result analysis.EffectStreamsForTest, fn moduleid.FunctionID, site cfg.SiteID) {
				result[fn][site] = []analysis.OpForTest{analysis.UseForTest{Place: analysis.PlaceForTest{Root: &symbols.Symbol{Name: "x"}}, Node: source.ParsedNodeID(1)}}
			},
			want: "is a use with no source location to report against",
		},
		{
			name: "operation naming an unknown node",
			damage: func(result analysis.EffectStreamsForTest, fn moduleid.FunctionID, site cfg.SiteID) {
				result[fn][site] = []analysis.OpForTest{analysis.WriteForTest{Place: analysis.PlaceForTest{Root: &symbols.Symbol{Name: "x"}}, Node: source.ParsedNodeID(999999)}}
			},
			want: "which is not in the typed THIR",
		},
		{
			name: "effects at a site the graph does not contain",
			damage: func(result analysis.EffectStreamsForTest, fn moduleid.FunctionID, site cfg.SiteID) {
				result[fn][cfg.SiteID{Block: 4242, Index: 7}] = []analysis.OpForTest{}
			},
			want: "which the graph does not contain",
		},
		{
			name: "effects for a function with no graph",
			damage: func(result analysis.EffectStreamsForTest, fn moduleid.FunctionID, site cfg.SiteID) {
				result[moduleid.FunctionID("missing-function")] = analysis.EffectSiteOpsForTest{}
			},
			want: "has derived effects but no control-flow graph",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, module := buildEffectsForTest(t, validationSource)
			fn, site := anySite(t, result)
			test.damage(result, fn, site)
			err := analysis.ValidateEffectsForTest(result, module.CFG, module.THIR)
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
	result, module := buildEffectsForTest(t, validationSource)
	fn, site := anySite(t, result)
	function := module.THIR.FunctionByID(fn)
	binding := function.Body.Stmts[0].(*thir.Binding)
	assignment := function.Body.Stmts[1].(*thir.Assign)
	target := assignment.Target
	root := analysis.PlaceForTest{Root: binding.Symbol}
	bindingID := binding.Source.NodeID
	assignID := assignment.Source.NodeID
	targetID := target.SourceInfo().NodeID
	for _, test := range []struct {
		name string
		ops  []analysis.OpForTest
		want string
	}{
		{"define source", []analysis.OpForTest{analysis.DefineForTest{Symbol: binding.Symbol, Source: binding, Node: assignID}}, "does not match node"},
		{"define value", []analysis.OpForTest{analysis.DefineForTest{Symbol: binding.Symbol, Source: binding, Node: bindingID, Value: assignID, ValueExpr: binding.Value}}, "unexpected node type"},
		{"parameter identity", []analysis.OpForTest{analysis.DefineForTest{Symbol: binding.Symbol, Node: function.Params[0].Source.NodeID, IsOnEntry: true}}, "not in typed THIR"},
		{"write target", []analysis.OpForTest{analysis.WriteForTest{Place: root, Node: assignID, Target: target, Owner: assignID}}, "unexpected node type"},
		{"write owner", []analysis.OpForTest{analysis.WriteForTest{Place: root, Node: targetID, Target: target, Owner: bindingID}}, "unexpected node type"},
		{"write value", []analysis.OpForTest{analysis.WriteForTest{Place: root, Node: targetID, Target: target, Owner: assignID, Value: assignID, ValueExpr: assignment.Value}}, "unexpected node type"},
		{"use", []analysis.OpForTest{analysis.UseForTest{Place: root, Node: assignID, Source: target, Location: target.SourceInfo().Location}}, "unexpected node type"},
		{"missing use source", []analysis.OpForTest{analysis.UseForTest{Place: root, Node: targetID, Location: target.SourceInfo().Location}}, "does not match node"},
		{"borrow operand", []analysis.OpForTest{analysis.BorrowForTest{Place: root, Source: target, Node: targetID, Operand: assignID, OperandExpr: target, Location: target.SourceInfo().Location}}, "unexpected node type"},
		{"iteration owner", []analysis.OpForTest{analysis.IterateForTest{Place: root, Source: target, Node: targetID, Loop: bindingID, Carrier: binding.Symbol, Location: target.SourceInfo().Location}}, "unexpected node type"},
		{"discard", []analysis.OpForTest{analysis.DiscardForTest{Place: root, Node: assignID, Source: target, Location: target.SourceInfo().Location}}, "unexpected node type"},
		{"call start", []analysis.OpForTest{analysis.CallBeginForTest{Node: targetID, Source: target}, analysis.CallEndForTest{Node: targetID}}, "unexpected node type"},
		{"temporary", []analysis.OpForTest{analysis.UseForTest{Place: analysis.PlaceForTest{Temporary: assignID, TemporaryExpr: target}, Node: targetID, Source: target, Location: target.SourceInfo().Location}}, "unexpected node type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			streams := analysis.EffectStreamsForTest{fn: analysis.EffectSiteOpsForTest{site: test.ops}}
			if err := analysis.ValidateEffectsForTest(streams, module.CFG, module.THIR); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsMissingTHIR(t *testing.T) {
	result, module := buildEffectsForTest(t, validationSource)
	if err := analysis.ValidateEffectsForTest(result, module.CFG, nil); err == nil || !strings.Contains(err.Error(), "typed THIR is missing") {
		t.Fatalf("Validate() = %v, want missing THIR", err)
	}
}

func TestValidateAcceptsEmptyStreams(t *testing.T) {
	if err := analysis.ValidateEffectsForTest(analysis.EffectStreamsForTest(nil), nil, nil); err != nil {
		t.Fatalf("Validate() = %v, want nil for an empty artifact", err)
	}
}

// anySite returns one derived-effects site so a damage case has somewhere to write.
func anySite(t *testing.T, streams analysis.EffectStreamsForTest) (moduleid.FunctionID, cfg.SiteID) {
	t.Helper()
	for fn, siteOps := range streams {
		for site := range siteOps {
			return fn, site
		}
	}
	t.Fatal("effect streams have no sites")
	return "", cfg.SiteID{}
}
