package ir

import (
	"fmt"
	"testing"
)

func TestTypeTableConcurrentInterningAndReads(t *testing.T) {
	types := NewTypeTable()
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
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
				if got, ok := types.LookupText(uniqueName); !ok || got != unique {
					results <- result{err: fmt.Sprintf("LookupText(%q) = (%d, %t), want (%d, true)", uniqueName, got, ok, unique)}
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
	nodeID, err := types.ReserveNamed(nodeShell)
	if err != nil {
		t.Fatal(err)
	}
	if _, complete := types.Type(nodeID); complete {
		t.Fatal("reserved named type was visible before completion")
	}
	if repeated, err := types.ReserveNamed(nodeShell); err != nil || repeated != nodeID {
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
	i32 := types.Intern(Type{Kind: TypeInteger, Signed: true, Bits: 32})
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
