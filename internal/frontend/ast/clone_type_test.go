package ast

import (
	"reflect"
	"slices"
	"testing"

	"compiler/internal/source"
)

func TestSubstituteExprNestedTypeOrderAndIsolation(t *testing.T) {
	// Deliberately exercise allocation orders that differ from Inspect order:
	// applied/path arguments precede names; interface returns precede receivers.
	length := &NumberLit{Value: "4"}
	element := &NamedType{Name: "Item"}
	array := &ArrayType{Len: length, Shape: ArrayFixed, Elem: element}
	boxName := &Ident{Name: "Box"}
	box := &AppliedType{Name: boxName, TypeArgs: []TypeExpr{array}}
	pathName := &Ident{Name: "pkg"}
	path := &ScopeResolution{Segments: []PathSegment{{Name: pathName, TypeArgs: []TypeExpr{box}}}}
	result := &OptionalType{Inner: path}
	origin := &Ident{Name: "self"}
	receiverName := &Ident{Name: "self"}
	receiverTarget := &NamedType{Name: "Self"}
	reference := &RefType{IsMutable: true, Target: receiverTarget}
	typeParam := &Ident{Name: "T"}
	paramName := &Ident{Name: "callback"}
	innerName := &Ident{Name: "value"}
	innerType := &NamedType{Name: "T"}
	// This name is also in the substitution map: type-contained defaults must
	// copy it, not substitute it or switch its provenance independently.
	innerDefault := &Ident{Name: "input"}
	returnType := &RawPtrType{}
	innerOrigin := &Ident{Name: "value"}
	function := &FuncType{
		Params: []Param{{Name: innerName, Type: innerType, Default: innerDefault, IsMutable: true}},
		Return: returnType, ReturnOrigins: &ReturnOriginClause{Sources: []*Ident{innerOrigin}},
	}
	paramDefault := &NoneLit{}
	methodName := &Ident{Name: "Read"}
	location := &source.Location{}
	iface := &InterfaceType{Methods: []TypeMethod{{
		Name: methodName, ReturnType: result,
		ReturnOrigins: &ReturnOriginClause{Sources: []*Ident{origin}, Location: location},
		Receiver:      &Param{Name: receiverName, Type: reference, IsMutable: true, MutableLocation: location},
		TypeParams:    []TypeParam{{Name: typeParam}},
		Params:        []Param{{Name: paramName, Type: function, Default: paramDefault}},
		Location:      location,
	}}, Location: location}
	value := &Ident{Name: "input"}
	expr := &AsExpr{Expr: value, TypeExpr: iface, Location: location}

	// Independent, explicit allocation-order oracle, not derived by traversing
	// the clone. Original IDs deliberately differ from generated ordinals.
	order := []Node{
		expr, value, iface, methodName, result, path, box, array, length, element,
		boxName, pathName, origin, receiverName, reference, receiverTarget,
		typeParam, paramName, function, innerName, innerType, innerDefault,
		returnType, innerOrigin, paramDefault,
	}
	originals := make(map[source.NodeID]Node)
	for i, node := range order {
		id := source.ParsedNodeID(uint64(100 + i))
		node.SetID(id)
		originals[id] = node
	}

	for _, fromArgument := range []bool{false, true} {
		name := "default"
		if fromArgument {
			name = "repeated-argument"
		}
		t.Run(name, func(t *testing.T) {
			var root Expr = expr
			var substitutions map[string]Expr
			copies, offset := 1, 0
			if fromArgument {
				root = &BinaryExpr{
					NodeIDHolder: NodeIDHolder{NodeID: source.ParsedNodeID(1)}, Op: "+",
					Left: &Ident{Name: "input"}, Right: &Ident{Name: "input"},
				}
				substitutions = map[string]Expr{"input": expr}
				copies, offset = 2, 1
			}
			owner := testGeneratedOwner()
			cloned, defaults, arguments := SubstituteExpr(owner, 7, root, substitutions)
			repeated, _, _ := SubstituteExpr(owner, 7, root, substitutions)
			if !slices.Equal(nodeIDs(cloned), nodeIDs(repeated)) {
				t.Fatal("identical expansion changed generated identities")
			}
			provenance := defaults
			trees := []Expr{cloned}
			if fromArgument {
				binary := cloned.(*BinaryExpr)
				trees = []Expr{binary.Left, binary.Right}
				provenance = arguments
				if len(defaults) != 1 || defaults[binary.ID()] != root.ID() {
					t.Fatalf("default provenance = %v; want only binary root", defaults)
				}
			} else if len(arguments) != 0 {
				t.Fatalf("default annotation leaked argument provenance: %v", arguments)
			}
			if len(provenance) != copies*len(order) {
				t.Fatalf("provenance count = %d, want %d", len(provenance), copies*len(order))
			}
			for copyIndex := range copies {
				for i, original := range order {
					id := source.GeneratedNodeID(owner, source.GeneratedDefaultArgument, 7, uint64(offset+copyIndex*len(order)+i+1))
					if got := provenance[id]; got != original.ID() {
						t.Fatalf("ordinal %d origin = %v, want %v (%T)", offset+copyIndex*len(order)+i+1, got, original.ID(), original)
					}
				}
			}

			seenNodes := make(map[Node]bool)
			seenIDs := make(map[source.NodeID]bool)
			for _, tree := range trees {
				var nodes []Node
				Inspect(tree, func(node Node) bool {
					if node == nil {
						return true
					}
					original := originals[provenance[node.ID()]]
					if original == nil || original == node || reflect.TypeOf(original) != reflect.TypeOf(node) {
						t.Fatalf("invalid clone %T/%v for original %T", node, node.ID(), original)
					}
					if seenNodes[node] || seenIDs[node.ID()] {
						t.Fatalf("shared node or duplicate ID: %T/%v", node, node.ID())
					}
					seenNodes[node], seenIDs[node.ID()] = true, true
					nodes = append(nodes, node)
					return true
				})
				if len(nodes) != len(order) {
					t.Fatalf("clone node count = %d, want %d", len(nodes), len(order))
				}
				// After testing generated IDs, normalize only the detached clone
				// so DeepEqual checks all metadata, including non-rendered fields.
				for _, node := range nodes {
					node.SetID(provenance[node.ID()])
				}
				if !reflect.DeepEqual(tree, expr) {
					t.Fatal("clone lost structure or metadata beyond generated IDs")
				}
				out := tree.(*AsExpr).TypeExpr.(*InterfaceType)
				method := &out.Methods[0]
				originalMethod := &iface.Methods[0]
				fn := method.Params[0].Type.(*FuncType)
				outPath := method.ReturnType.(*OptionalType).Inner.(*ScopeResolution)
				outBox := outPath.Segments[0].TypeArgs[0].(*AppliedType)
				if method == originalMethod || method.Receiver == originalMethod.Receiver ||
					method.ReturnOrigins == originalMethod.ReturnOrigins || fn.ReturnOrigins == function.ReturnOrigins ||
					&method.TypeParams[0] == &originalMethod.TypeParams[0] || &method.Params[0] == &originalMethod.Params[0] ||
					&method.ReturnOrigins.Sources[0] == &originalMethod.ReturnOrigins.Sources[0] ||
					&fn.Params[0] == &function.Params[0] || &fn.ReturnOrigins.Sources[0] == &function.ReturnOrigins.Sources[0] ||
					&outPath.Segments[0] == &path.Segments[0] || &outPath.Segments[0].TypeArgs[0] == &path.Segments[0].TypeArgs[0] ||
					&outBox.TypeArgs[0] == &box.TypeArgs[0] {
					t.Fatal("clone shares mutable syntax container with original")
				}
				if out.Location != location || method.Location != location || method.Receiver.MutableLocation != location {
					t.Fatal("clone replaced shared source locations")
				}
				method.TypeParams[0].Name.Name = "Changed"
				method.Params[0].Default = nil
				method.ReturnOrigins.Sources[0] = &Ident{Name: "Changed"}
				outBox.TypeArgs[0] = &RawPtrType{}
				if typeParam.Name != "T" || originalMethod.Params[0].Default != paramDefault ||
					originalMethod.ReturnOrigins.Sources[0] != origin || box.TypeArgs[0] != array {
					t.Fatal("mutating clone changed source tree")
				}
			}
		})
	}
}
