package effect_test

import (
	"strings"
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
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
	if err := result.Validate(module.CFG, module.TypedASTNodes); err != nil {
		t.Fatalf("Validate() = %v, want nil for a published artifact", err)
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
				result[fn][site] = []effect.Op{effect.Write{Symbol: &symbols.Symbol{Name: "x"}, Node: 999999}}
			},
			want: "which is not in the typed AST",
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
			err := result.Validate(module.CFG, module.TypedASTNodes)
			if err == nil {
				t.Fatalf("Validate() = nil, want a report containing %q", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want a report containing %q", err, test.want)
			}
		})
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
