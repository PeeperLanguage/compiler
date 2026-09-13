package flowresult

import (
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
)

func TestOriginsArePublishedAndMergedAtomically(t *testing.T) {
	result := New()
	first := symbols.New("first", symbols.SymbolVar, nil, nil)
	second := symbols.New("second", symbols.SymbolVar, nil, nil)
	third := symbols.New("third", symbols.SymbolVar, nil, nil)
	const id ast.NodeID = 7

	storage := []place.Origin{{Root: first}}
	value := []place.Origin{{Root: second}}
	result.RecordOrigins(id, storage, value)

	// Publication owns its evidence rather than aliasing producer scratch slices.
	storage[0].Root = third
	value[0].Root = third
	got, ok := result.Origins(id)
	if !ok || got.Storage[0].Root != first || got.Value[0].Root != second {
		t.Fatalf("recorded origins = %#v", got)
	}

	result.MergeOrigins(id, []place.Origin{{Root: third}}, []place.Origin{{Root: third}})
	got, ok = result.Origins(id)
	if !ok || len(got.Storage) != 2 || len(got.Value) != 2 {
		t.Fatalf("merged origins = %#v", got)
	}

	// Queries return owned snapshots, so consumers cannot mutate the result.
	got.Storage[0].Root = third
	again, _ := result.Origins(id)
	if again.Storage[0].Root != first {
		t.Fatal("origin query leaked mutable backing storage")
	}
}
