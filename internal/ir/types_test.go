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
				unique := types.Intern(Type{Kind: TypeNamed, Name: uniqueName})
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
