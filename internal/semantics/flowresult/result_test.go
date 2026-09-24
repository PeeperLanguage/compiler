package flowresult

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
)

func TestOriginsArePublishedAndMergedAtomically(t *testing.T) {
	result := New()
	first := symbols.New("first", symbols.SymbolVar, nil, nil)
	second := symbols.New("second", symbols.SymbolVar, nil, nil)
	third := symbols.New("third", symbols.SymbolVar, nil, nil)
	const id ir.NodeID = 7

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

func TestAggregateSlotsOwnSnapshotsAndDistinguishEmptyAggregate(t *testing.T) {
	result := New()
	const id ir.NodeID = 11
	slots := []AggregateSlot{{
		Projection: place.OriginProjection{
			Kind:  place.OriginField,
			Field: "value",
		},
	}}
	result.RecordAggregateSlots(id, slots)

	// Publication and queries own their slice so callers cannot mutate Flow's
	// aggregate decomposition accidentally.
	slots[0].Projection.Field = "changed"
	got, ok := result.AggregateSlots(id)
	if !ok || len(got) != 1 || got[0].Projection.Field != "value" {
		t.Fatalf("aggregate slots = %#v", got)
	}
	got[0].Projection.Field = "changed"
	again, _ := result.AggregateSlots(id)
	if again[0].Projection.Field != "value" {
		t.Fatal("aggregate slot query leaked mutable backing storage")
	}

	const empty ir.NodeID = 14
	result.RecordAggregateSlots(empty, nil)
	if got, ok := result.AggregateSlots(empty); !ok || len(got) != 0 {
		t.Fatalf("empty aggregate = %#v, %v", got, ok)
	}
	if _, ok := result.AggregateSlots(15); ok {
		t.Fatal("missing aggregate evidence reported as present")
	}
}
