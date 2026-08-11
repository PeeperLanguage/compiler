package ast

import "testing"

func TestSubstituteExprClonesEachArgumentOccurrenceWithFreshIDs(t *testing.T) {
	argument := &AsExpr{
		NodeIDHolder: NodeIDHolder{NodeID: 10},
		Expr: &SelectorExpr{
			NodeIDHolder: NodeIDHolder{NodeID: 11},
			Expr:         &Ident{NodeIDHolder: NodeIDHolder{NodeID: 12}, Name: "value"},
			Name:         &Ident{NodeIDHolder: NodeIDHolder{NodeID: 13}, Name: "field"},
		},
		TypeExpr: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: 14}, Name: "i32"},
	}
	defaultExpr := &BinaryExpr{
		NodeIDHolder: NodeIDHolder{NodeID: 1},
		Left:         &Ident{NodeIDHolder: NodeIDHolder{NodeID: 2}, Name: "input"},
		Op:           "+",
		Right:        &Ident{NodeIDHolder: NodeIDHolder{NodeID: 3}, Name: "input"},
	}

	expanded, defaultClones, argumentClones := SubstituteExpr(defaultExpr, map[string]Expr{"input": argument})
	binary := expanded.(*BinaryExpr)
	if binary.Left == binary.Right || binary.Left == argument || binary.Right == argument {
		t.Fatal("substituted occurrences must be separate trees")
	}
	if len(defaultClones) != 1 || len(argumentClones) != 10 {
		t.Fatalf("clone provenance = %d default, %d argument; want 1 and 10", len(defaultClones), len(argumentClones))
	}

	seen := make(map[NodeID]struct{})
	Inspect(expanded, func(node Node) bool {
		if node == nil {
			return true
		}
		if node.ID()&(1<<31) == 0 {
			t.Fatalf("node %T kept non-synthetic ID %d", node, node.ID())
		}
		if _, duplicate := seen[node.ID()]; duplicate {
			t.Fatalf("duplicate cloned NodeID %d", node.ID())
		}
		seen[node.ID()] = struct{}{}
		return true
	})
}

func TestSubstituteExprSeparatesDefaultAndArgumentProvenance(t *testing.T) {
	defaultExpr := &AddressExpr{
		NodeIDHolder: NodeIDHolder{NodeID: 1},
		Mode:         AddressShared,
		Expr:         &Ident{NodeIDHolder: NodeIDHolder{NodeID: 2}, Name: "input"},
	}
	argument := &Ident{NodeIDHolder: NodeIDHolder{NodeID: 7}, Name: "caller"}

	_, defaultClones, argumentClones := SubstituteExpr(defaultExpr, map[string]Expr{"input": argument})
	for _, original := range defaultClones {
		if original != 1 {
			t.Fatalf("default provenance contains caller/default placeholder ID %d", original)
		}
	}
	for _, original := range argumentClones {
		if original != 7 {
			t.Fatalf("argument provenance contains default ID %d", original)
		}
	}
}

func TestSubstituteExprClonesOpenEndedRanges(t *testing.T) {
	tests := []struct {
		name      string
		start     Expr
		end       Expr
		wantStart bool
		wantEnd   bool
	}{
		{name: "full", wantStart: false, wantEnd: false},
		{name: "prefix", end: &NumberLit{NodeIDHolder: NodeIDHolder{NodeID: 2}, Value: "2"}, wantStart: false, wantEnd: true},
		{name: "suffix", start: &NumberLit{NodeIDHolder: NodeIDHolder{NodeID: 2}, Value: "2"}, wantStart: true, wantEnd: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rangeExpr := &RangeExpr{
				NodeIDHolder: NodeIDHolder{NodeID: 1},
				Start:        test.start,
				End:          test.end,
				EndExclusive: true,
			}
			cloned, defaultClones, argumentClones := SubstituteExpr(rangeExpr, nil)
			out, ok := cloned.(*RangeExpr)
			if !ok {
				t.Fatalf("clone = %T, want *RangeExpr", cloned)
			}
			if (out.Start != nil) != test.wantStart || (out.End != nil) != test.wantEnd {
				t.Fatalf("range bounds = start %v, end %v; want start %v, end %v", out.Start != nil, out.End != nil, test.wantStart, test.wantEnd)
			}
			if out.ID() == rangeExpr.ID() || (out.Start != nil && out.Start.ID() == test.start.ID()) || (out.End != nil && out.End.ID() == test.end.ID()) {
				t.Fatal("range clone reused source NodeID")
			}
			want := 1
			if test.wantStart {
				want++
			}
			if test.wantEnd {
				want++
			}
			if len(defaultClones) != want || len(argumentClones) != 0 {
				t.Fatalf("clone provenance = %d default, %d argument; want %d default, 0 argument", len(defaultClones), len(argumentClones), want)
			}
		})
	}
}
