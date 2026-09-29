package ast

import (
	"reflect"
	"slices"
	"testing"

	"compiler/internal/moduleid"
	"compiler/internal/source"
)

func testGeneratedOwner() source.NodeID {
	return source.FunctionNodeID(moduleid.FunctionID("test-function"), 1)
}

func nodeIDs(node Node) []source.NodeID {
	ids := make([]source.NodeID, 0)
	Inspect(node, func(node Node) bool {
		if node != nil {
			ids = append(ids, node.ID())
		}
		return true
	})
	return ids
}

func TestSubstituteExprClonesEachArgumentOccurrenceWithFreshIDs(t *testing.T) {
	argument := &AsExpr{
		NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(10)},
		Expr: &SelectorExpr{
			NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(11)},
			Expr:         &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(12)}, Name: "value"},
			Name:         &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(13)}, Name: "field"},
		},
		TypeExpr: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(14)}, Name: "i32"},
	}
	defaultExpr := &BinaryExpr{
		NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)},
		Left:         &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "input"},
		Op:           "+",
		Right:        &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(3)}, Name: "input"},
	}

	expanded, defaultClones, argumentClones := SubstituteExpr(testGeneratedOwner(), 1, defaultExpr, map[string]Expr{"input": argument})
	repeated, _, _ := SubstituteExpr(testGeneratedOwner(), 1, defaultExpr, map[string]Expr{"input": argument})
	if !slices.Equal(nodeIDs(expanded), nodeIDs(repeated)) {
		t.Fatalf("repeated clone identities differ: %v and %v", nodeIDs(expanded), nodeIDs(repeated))
	}
	differentSlot, _, _ := SubstituteExpr(testGeneratedOwner(), 2, defaultExpr, map[string]Expr{"input": argument})
	if slices.Equal(nodeIDs(expanded), nodeIDs(differentSlot)) {
		t.Fatalf("different parameter slots share clone identities: %v", nodeIDs(expanded))
	}
	binary := expanded.(*BinaryExpr)
	if binary.Left == binary.Right || binary.Left == argument || binary.Right == argument {
		t.Fatal("substituted occurrences must be separate trees")
	}
	if len(defaultClones) != 1 || len(argumentClones) != 10 {
		t.Fatalf("clone provenance = %d default, %d argument; want 1 and 10", len(defaultClones), len(argumentClones))
	}

	seen := make(map[source.NodeID]struct{})
	Inspect(expanded, func(node Node) bool {
		if node == nil {
			return true
		}
		if !node.ID().IsGenerated() {
			t.Fatalf("node %T kept non-generated ID %v", node, node.ID())
		}
		if _, duplicate := seen[node.ID()]; duplicate {
			t.Fatalf("duplicate cloned source.NodeID %v", node.ID())
		}
		seen[node.ID()] = struct{}{}
		return true
	})
}

func TestSubstituteExprSeparatesDefaultAndArgumentProvenance(t *testing.T) {
	defaultExpr := &AddressExpr{
		NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)},
		Mode:         AddressShared,
		Expr:         &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "input"},
	}
	argument := &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(7)}, Name: "caller"}

	_, defaultClones, argumentClones := SubstituteExpr(testGeneratedOwner(), 1, defaultExpr, map[string]Expr{"input": argument})
	for _, original := range defaultClones {
		if original != source.ParsedNodeID(1) {
			t.Fatalf("default provenance contains caller/default placeholder ID %v", original)
		}
	}
	for _, original := range argumentClones {
		if original != source.ParsedNodeID(7) {
			t.Fatalf("argument provenance contains default ID %v", original)
		}
	}
}

func TestSubstituteExprClonesEveryTypeExpression(t *testing.T) {
	tests := []struct {
		name string
		typ  TypeExpr
	}{
		{"named", &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Name: "i32"}},
		{"applied", &AppliedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Name: &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "Box"}, TypeArgs: []TypeExpr{&NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(3)}, Name: "i32"}}}},
		{"owned-pointer", &OwnedPtrType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Target: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "Item"}}},
		{"raw-pointer", &RawPtrType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}}},
		{"reference", &RefType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, IsMutable: true, Target: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "Item"}}},
		{"optional", &OptionalType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Inner: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "Item"}}},
		{"array", &ArrayType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Len: &NumberLit{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Value: "4"}, Elem: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(3)}, Name: "Item"}}},
		{"function", &FuncType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Params: []Param{{Name: &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "value"}, Type: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(3)}, Name: "Item"}}}, Return: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(4)}, Name: "Result"}}},
		{"struct", &StructType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Fields: []TypeField{{Name: &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "value"}, Type: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(3)}, Name: "Item"}}}}},
		{"interface", &InterfaceType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Methods: []TypeMethod{{Name: &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "read"}, Params: []Param{{Type: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(3)}, Name: "Item"}}}, ReturnType: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(4)}, Name: "Result"}}}}},
		{"enum", &EnumType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Variants: []EnumVariant{{Name: &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "some"}, Payload: &NamedType{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(3)}, Name: "Item"}}}}},
		{"scope-resolution", &ScopeResolution{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Segments: []PathSegment{{Name: &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Name: "pkg"}}, {Name: &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(3)}, Name: "Item"}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expr := &AsExpr{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(100)}, Expr: &Ident{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(101)}, Name: "value"}, TypeExpr: test.typ}
			originalNodes := make(map[Node]source.NodeID)
			Inspect(test.typ, func(node Node) bool {
				if node != nil {
					originalNodes[node] = node.ID()
				}
				return true
			})
			cloned, _, _ := SubstituteExpr(testGeneratedOwner(), 1, expr, nil)
			out, ok := cloned.(*AsExpr)
			if !ok || out == nil {
				t.Fatalf("clone = %T, want non-nil *AsExpr", cloned)
			}
			if reflect.TypeOf(out.TypeExpr) != reflect.TypeOf(test.typ) {
				t.Fatalf("type clone = %T, want %T", out.TypeExpr, test.typ)
			}
			if out.TypeExpr == test.typ {
				t.Fatal("type clone reused original node")
			}
			if TypeText(out.TypeExpr) != TypeText(test.typ) {
				t.Fatalf("type text = %q, want %q", TypeText(out.TypeExpr), TypeText(test.typ))
			}
			seen := make(map[source.NodeID]struct{})
			Inspect(out.TypeExpr, func(node Node) bool {
				if node == nil {
					return true
				}
				if _, shared := originalNodes[node]; shared {
					t.Fatalf("clone shares nested %T with original", node)
				}
				if !node.ID().IsGenerated() {
					t.Fatalf("%T retained source ID %v", node, node.ID())
				}
				if _, exists := seen[node.ID()]; exists {
					t.Fatalf("duplicate generated ID %v", node.ID())
				}
				seen[node.ID()] = struct{}{}
				return true
			})
			if len(seen) != len(originalNodes) {
				t.Fatalf("clone has %d nodes, want %d", len(seen), len(originalNodes))
			}
			for node, id := range originalNodes {
				if node.ID() != id {
					t.Fatalf("cloning mutated original %T identity", node)
				}
			}
		})
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
		{name: "prefix", end: &NumberLit{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Value: "2"}, wantStart: false, wantEnd: true},
		{name: "suffix", start: &NumberLit{NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(2)}, Value: "2"}, wantStart: true, wantEnd: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rangeExpr := &RangeExpr{
				NodeIDHolder:   NodeIDHolder{NodeID: source.ParsedNodeID(1)},
				Start:          test.start,
				End:            test.end,
				IsEndExclusive: true,
			}
			cloned, defaultClones, argumentClones := SubstituteExpr(testGeneratedOwner(), 1, rangeExpr, nil)
			out, ok := cloned.(*RangeExpr)
			if !ok {
				t.Fatalf("clone = %T, want *RangeExpr", cloned)
			}
			if (out.Start != nil) != test.wantStart || (out.End != nil) != test.wantEnd {
				t.Fatalf("range bounds = start %v, end %v; want start %v, end %v", out.Start != nil, out.End != nil, test.wantStart, test.wantEnd)
			}
			if out.ID() == rangeExpr.ID() || (out.Start != nil && out.Start.ID() == test.start.ID()) || (out.End != nil && out.End.ID() == test.end.ID()) {
				t.Fatal("range clone reused source source.NodeID")
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
