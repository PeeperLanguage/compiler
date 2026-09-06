package effect_test

import (
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
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
				result[fn][site] = []effect.Op{effect.Write{Place: effect.Place{Root: &symbols.Symbol{Name: "x"}}, Node: 999999}}
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

func TestValidateRejectsWrongExpressionIdentity(t *testing.T) {
	result, module := buildEffects(t, validationSource)
	fn, site := anySite(t, result)
	function := module.TypedASTNodes[ast.NodeID(fn)].(*ast.FnDecl)
	expr := function.Params[0].Name
	target := function.Body.Stmts[1].(*ast.AssignStmt).Target
	root := effect.Place{Root: &symbols.Symbol{Name: "count"}}
	for _, test := range []struct {
		name string
		ops  []effect.Op
	}{
		{"define value", []effect.Op{effect.Define{Symbol: root.Root, Node: function.ID(), Value: expr.ID(), Initialized: true}}},
		{"write target", []effect.Op{effect.Write{Place: root, Node: expr.ID(), Owner: function.Body.ID()}}},
		{"write value", []effect.Op{effect.Write{Place: root, Node: target.ID(), Owner: function.Body.ID(), Value: expr.ID()}}},
		{"use", []effect.Op{effect.Use{Place: root, Node: expr.ID(), Location: ast.LocOf(expr)}}},
		{"borrow", []effect.Op{effect.Borrow{Place: root, Node: expr.ID(), Operand: target.ID(), Location: ast.LocOf(expr)}}},
		{"borrow operand", []effect.Op{effect.Borrow{Place: root, Node: target.ID(), Operand: expr.ID(), Location: ast.LocOf(target)}}},
		{"iteration", []effect.Op{effect.Iterate{Place: root, Node: expr.ID(), Loop: function.Body.ID(), Carrier: root.Root, Location: ast.LocOf(expr)}}},
		{"discard", []effect.Op{effect.Discard{Place: root, Node: expr.ID(), Location: ast.LocOf(expr)}}},
		{"call start", []effect.Op{effect.CallBegin{Node: expr.ID(), Location: ast.LocOf(expr)}, effect.CallEnd{Node: expr.ID()}}},
		{"temporary", []effect.Op{effect.Use{Place: effect.Place{Temporary: expr.ID()}, Node: target.ID(), Location: ast.LocOf(target)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			published := effect.Result{fn: effect.SiteOps{site: test.ops}}
			if err := published.Validate(module.CFG, module.TypedASTNodes); err != nil {
				t.Fatalf("well-shaped operation rejected: %v", err)
			}
			nodes := make(map[ast.NodeID]ast.Node, len(module.TypedASTNodes))
			for id, node := range module.TypedASTNodes {
				nodes[id] = node
			}
			nodes[expr.ID()] = &ast.BlockStmt{NodeIDHolder: ast.NodeIDHolder{NodeID: expr.ID()}}
			if err := published.Validate(module.CFG, nodes); err == nil || !strings.Contains(err.Error(), "unexpected node type") {
				t.Fatalf("wrong expression identity: got %v", err)
			}
		})
	}
}

func TestValidateRejectsNilIndexedNode(t *testing.T) {
	result, module := buildEffects(t, validationSource)
	for id := range module.TypedASTNodes {
		module.TypedASTNodes[id] = nil
	}
	if err := result.Validate(module.CFG, module.TypedASTNodes); err == nil {
		t.Fatal("nil indexed nodes accepted")
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
