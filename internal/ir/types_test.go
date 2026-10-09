package ir

import (
	"fmt"
	"testing"
)

func TestTypeTableConcurrentInterningAndReads(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, IsSigned: true, Bits: 32})
	usize := types.Intern(Type{Kind: TypeInteger, Bits: 64})
	types.SetIndexType(usize)

	const workers = 16
	const rounds = 64
	type result struct {
		shared TypeID
		err    string
	}
	start := make(chan struct{})
	results := make(chan result, workers)

	for worker := range workers {
		go func() {
			<-start
			var shared TypeID
			for round := range rounds {
				shared = types.Intern(Type{Kind: TypeStruct, Fields: []TypeField{{Name: "value", Type: i32}}})
				uniqueName := fmt.Sprintf("Worker%dRound%d", worker, round)
				unique := types.Intern(Type{Kind: TypeStruct, Name: uniqueName, Identity: "test::" + uniqueName})
				if got := types.Text(unique); got != uniqueName {
					results <- result{err: fmt.Sprintf("Text(%d) = %q, want %q", unique, got, uniqueName)}
					return
				}
				if got := types.ABIKey(unique); got != "struct:test::"+uniqueName {
					results <- result{err: fmt.Sprintf("ABIKey(%d) = %q, want %q", unique, got, "struct:test::"+uniqueName)}
					return
				}
				if typ, ok := types.Type(shared); !ok || typ.Kind != TypeStruct {
					results <- result{err: fmt.Sprintf("Type(%d) = (%#v, %t), want struct", shared, typ, ok)}
					return
				}
				if got := types.IndexType(); got != usize {
					results <- result{err: fmt.Sprintf("IndexType() = %d, want %d", got, usize)}
					return
				}
			}
			results <- result{shared: shared}
		}()
	}

	close(start)
	var shared TypeID
	for range workers {
		result := <-results
		if result.err != "" {
			t.Fatal(result.err)
		}
		if shared == InvalidType {
			shared = result.shared
		} else if result.shared != shared {
			t.Fatalf("shared descriptor IDs differ: got %d and %d", shared, result.shared)
		}
	}
}

func TestTypeTableTypeReturnsDetachedDescriptor(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, IsSigned: true, Bits: 32})
	u64 := types.Intern(Type{Kind: TypeInteger, Bits: 64})

	id := types.Intern(Type{
		Kind:   TypeStruct,
		Fields: []TypeField{{Name: "field", Type: i32}},
		Methods: []TypeMethod{{
			Name:   "method",
			Params: []TypeField{{Name: "param", Type: i32}},
			Return: i32,
		}},
		Params: []TypeID{i32},
		Cases:  []VariantCase{{Name: "case", Payload: i32}},
	})

	descriptor, ok := types.Type(id)
	if !ok {
		t.Fatal("interned descriptor was not published")
	}
	descriptor.Fields[0] = TypeField{Name: "changed-field", Type: u64}
	descriptor.Methods[0].Name = "changed-method"
	descriptor.Methods[0].Params[0] = TypeField{Name: "changed-param", Type: u64}
	descriptor.Params[0] = u64
	descriptor.Cases[0] = VariantCase{Name: "changed-case", Payload: u64}

	got, ok := types.Type(id)
	if !ok {
		t.Fatal("interned descriptor disappeared after caller mutation")
	}
	if got.Fields[0] != (TypeField{Name: "field", Type: i32}) {
		t.Fatalf("Fields mutation leaked into TypeTable: %#v", got.Fields)
	}
	if got.Methods[0].Name != "method" {
		t.Fatalf("Methods mutation leaked into TypeTable: %#v", got.Methods)
	}
	if got.Methods[0].Params[0] != (TypeField{Name: "param", Type: i32}) {
		t.Fatalf("method Params mutation leaked into TypeTable: %#v", got.Methods[0].Params)
	}
	if got.Params[0] != i32 {
		t.Fatalf("Params mutation leaked into TypeTable: %#v", got.Params)
	}
	if got.Cases[0] != (VariantCase{Name: "case", Payload: i32}) {
		t.Fatalf("Cases mutation leaked into TypeTable: %#v", got.Cases)
	}
}

func TestTypeTableUsesLanguageNamesForStringTypes(t *testing.T) {
	types := NewTypeTable()
	if got := types.Text(types.Intern(Type{Kind: TypeString})); got != "str" {
		t.Fatalf("string type text = %q, want str", got)
	}
}

func TestTypeTablePreservesInterfaceReceiverIdentity(t *testing.T) {
	types := NewTypeTable()
	void := types.Intern(Type{Kind: TypeVoid})
	shared := types.Intern(Type{Kind: TypeInterface, Methods: []TypeMethod{{
		Name: "read", Receiver: MethodReceiverShared, Return: void,
	}}})
	mutable := types.Intern(Type{Kind: TypeInterface, Methods: []TypeMethod{{
		Name: "read", Receiver: MethodReceiverMutable, Return: void,
	}}})
	if shared == mutable {
		t.Fatal("shared and mutable interface receivers shared one TypeID")
	}
	if types.Text(shared) != "iface{read(&Self)}" || types.Text(mutable) != "iface{read(&mut Self)}" {
		t.Fatalf("interface receiver text = %q and %q", types.Text(shared), types.Text(mutable))
	}
}

func TestTypeTableReservesAndCompletesRecursiveNamedComposite(t *testing.T) {
	types := NewTypeTable()
	nodeShell := Type{Kind: TypeStruct, Name: "Node", Identity: "test::Node"}
	nodeID, err := types.ReserveNamed(nodeShell, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, complete := types.Type(nodeID); complete {
		t.Fatal("reserved named type was visible before completion")
	}
	if got := types.ABIKey(nodeID); got != "struct:test::Node" {
		t.Fatalf("reserved recursive ABI key = %q, want struct:test::Node", got)
	}
	if repeated, err := types.ReserveNamed(nodeShell, nil); err != nil || repeated != nodeID {
		t.Fatalf("repeated reservation = (%d, %v), want (%d, nil)", repeated, err, nodeID)
	}
	ownedNode := types.Intern(Type{Kind: TypeOwnedPtr, Elem: nodeID})
	optionalNode := types.Intern(OptionalVariant(ownedNode))
	node := nodeShell
	node.Fields = []TypeField{{Name: "next", Type: optionalNode}}
	if err := types.CompleteNamed(nodeID, node); err != nil {
		t.Fatal(err)
	}
	completed, ok := types.Type(nodeID)
	if !ok || len(completed.Fields) != 1 || completed.Fields[0].Type != optionalNode {
		t.Fatalf("completed recursive descriptor = %#v", completed)
	}
	if got := types.Text(nodeID); got != "Node" {
		t.Fatalf("recursive type text = %q, want Node", got)
	}
	if got := types.ABIKey(nodeID); got != "struct:test::Node" {
		t.Fatalf("recursive ABI key = %q", got)
	}
	conflict := node
	conflict.Fields = []TypeField{{Name: "other", Type: optionalNode}}
	if err := types.CompleteNamed(nodeID, conflict); err == nil {
		t.Fatal("conflicting named type completion succeeded")
	}
}

func TestTypeTableInternsTaggedVariantIdentityAndCases(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, IsSigned: true, Bits: 32})
	str := types.Intern(Type{Kind: TypeString})

	optionalID := types.Intern(OptionalVariant(i32))
	optional, ok := types.Type(optionalID)
	if !ok || optional.Kind != TypeVariant || optional.Family != VariantFamilyOptional {
		t.Fatalf("optional variant = (%#v, %t)", optional, ok)
	}
	if len(optional.Cases) != 2 || optional.Cases[OptionalAbsentCase].Payload != InvalidType ||
		optional.Cases[OptionalPresentCase].Payload != i32 {
		t.Fatalf("optional cases = %#v", optional.Cases)
	}
	if got := types.Text(optionalID); got != "?i32" {
		t.Fatalf("optional text = %q, want ?i32", got)
	}

	result := Type{
		Kind: TypeVariant, Family: VariantFamilyNamed, Name: "Result<i32>", Identity: "app::Result<i32>",
		Cases: []VariantCase{
			{Name: "Ok", Payload: i32},
			{Name: "Error", Payload: str},
			{Name: "Pending"},
		},
	}
	resultID := types.Intern(result)
	otherID := types.Intern(Type{
		Kind: TypeVariant, Family: VariantFamilyNamed, Name: "Result<i32>", Identity: "other::Result<i32>",
		Cases: result.Cases,
	})
	if resultID == otherID {
		t.Fatal("nominally distinct variants shared one TypeID")
	}
	if types.Text(resultID) != "Result<i32>" || types.Text(otherID) != "Result<i32>" {
		t.Fatalf("named variant text = %q and %q", types.Text(resultID), types.Text(otherID))
	}
	if types.ABIKey(resultID) == types.ABIKey(otherID) {
		t.Fatal("nominally distinct variants shared one ABI key")
	}
}

func reserveDisplayedStruct(t *testing.T, types *TypeTable, name, display string) TypeID {
	t.Helper()
	shell := Type{Kind: TypeStruct, Name: name, Identity: "test::" + display}
	id, err := types.ReserveNamed(shell, func() string { return display })
	if err != nil {
		t.Fatal(err)
	}
	if err := types.CompleteNamed(id, shell); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNamedTypeIDsFollowDisplayNameNotInternOrder(t *testing.T) {
	names := []string{"local:app/zeta::Z", "core:core@global::Str", "local:app/alpha::Counter", "local:app/beta::Counter"}
	var orders [][]string
	for _, order := range [][]string{names, {names[3], names[2], names[1], names[0]}} {
		types := NewTypeTable()
		for _, display := range order {
			reserveDisplayedStruct(t, types, "T", display)
		}
		var got []string
		for _, id := range types.NamedTypeIDs() {
			got = append(got, types.DisplayName(id))
		}
		orders = append(orders, got)
	}
	want := fmt.Sprint([]string{"core:core@global::Str", "local:app/alpha::Counter", "local:app/beta::Counter", "local:app/zeta::Z"})
	for _, got := range orders {
		if fmt.Sprint(got) != want {
			t.Fatalf("NamedTypeIDs display order = %v, want %v", got, want)
		}
	}
}

func TestDisplayNameQualifiesNestedNamedTypes(t *testing.T) {
	types := NewTypeTable()
	counter := reserveDisplayedStruct(t, types, "Counter", "local:app/alpha::Counter")
	ref := types.Intern(Type{Kind: TypeReference, IsMutable: true, Elem: counter})
	optional := types.Intern(OptionalVariant(ref))
	if got := types.DisplayName(optional); got != "?&mut local:app/alpha::Counter" {
		t.Fatalf("DisplayName = %q", got)
	}
	if got := types.Text(optional); got != "?&mut Counter" {
		t.Fatalf("Text = %q, want unqualified diagnostics text", got)
	}
}

func TestReserveNamedBuildsDisplayNameOnlyForNewTypes(t *testing.T) {
	types := NewTypeTable()
	calls := 0
	shell := Type{Kind: TypeStruct, Name: "Counter", Identity: "test::Counter"}
	for range 3 {
		if _, err := types.ReserveNamed(shell, func() string { calls++; return "local:app/alpha::Counter" }); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("display name built %d times, want 1", calls)
	}
}

func TestNamedTypeIDsRejectSharedDisplayName(t *testing.T) {
	types := NewTypeTable()
	for _, identity := range []string{"test::a", "test::b"} {
		shell := Type{Kind: TypeStruct, Name: "T", Identity: identity}
		id, err := types.ReserveNamed(shell, func() string { return "local:app::T" })
		if err != nil {
			t.Fatal(err)
		}
		if err := types.CompleteNamed(id, shell); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("NamedTypeIDs accepted two named types with one display name")
		}
	}()
	types.NamedTypeIDs()
}
