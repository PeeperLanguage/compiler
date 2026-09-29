package analysis

import (
	"testing"

	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

func TestOriginsArePublishedAndMergedAtomically(t *testing.T) {
	m := newModule()
	id := source.ParsedNodeID(1)
	a := &symbols.Symbol{Name: "a"}
	b := &symbols.Symbol{Name: "b"}
	m.RecordOrigins(id, []place.Origin{{Root: a}}, []place.Origin{{Root: a}})
	m.MergeOrigins(id, []place.Origin{{Root: b}}, []place.Origin{{Root: b}})
	got, ok := m.Origins(id)
	if !ok || len(got.Storage) != 2 || len(got.Value) != 2 {
		t.Fatalf("origins = %#v, %v; want two merged storage/value origins", got, ok)
	}
	got.Storage[0].Root = b
	again, _ := m.Origins(id)
	if again.Storage[0].Root == b {
		t.Fatal("Origins returned mutable backing storage")
	}
}

func TestAggregateSlotsDistinguishKnownEmptyFromMissing(t *testing.T) {
	m := newModule()
	emptyID := source.ParsedNodeID(1)
	missingID := source.ParsedNodeID(2)
	m.RecordAggregateSlots(emptyID, []AggregateSlot{})
	if slots, ok := m.AggregateSlots(emptyID); !ok || len(slots) != 0 {
		t.Fatalf("known empty aggregate = %#v, %v", slots, ok)
	}
	if slots, ok := m.AggregateSlots(missingID); ok || slots != nil {
		t.Fatalf("missing aggregate = %#v, %v", slots, ok)
	}
}
