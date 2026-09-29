package analysis

import (
	"testing"

	"compiler/internal/ir/cfg"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

func TestOriginsArePublishedAndMergedAtomically(t *testing.T) {
	m := newModule()
	id := source.ParsedNodeID(1)
	a := &symbols.Symbol{Name: "a"}
	b := &symbols.Symbol{Name: "b"}
	m.recordOrigins(id, []place.Origin{{Root: a}}, []place.Origin{{Root: a}})
	m.mergeOrigins(id, []place.Origin{{Root: b}}, []place.Origin{{Root: b}})
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

func TestPayloadAndCaseTestReturnDetachedSlices(t *testing.T) {
	m := newModule()
	payloadID := source.ParsedNodeID(1)
	testID := source.ParsedNodeID(2)
	root := &symbols.Symbol{Name: "value"}
	payload := PayloadAccess{
		CarrierOrigins: []place.Origin{{Root: root, Projections: []place.OriginProjection{{Kind: place.OriginVariantPayload, Case: 0}}}},
		Cases:          []int{0},
	}
	test := CaseTest{SubjectID: payloadID, Case: 0, CaseCount: 1, PayloadPath: []int{0}}
	m.recordPayload(payloadID, payload)
	m.recordCaseTest(testID, test)

	payload.CarrierOrigins[0].Root = nil
	payload.Cases[0] = 9
	test.PayloadPath[0] = 9
	publishedPayload, _ := m.Payload(payloadID)
	publishedTest, _ := m.CaseTest(testID)
	if publishedPayload.CarrierOrigins[0].Root != root || publishedPayload.Cases[0] != 0 || publishedTest.PayloadPath[0] != 0 {
		t.Fatalf("producer retained mutable analysis storage: payload=%#v test=%#v", publishedPayload, publishedTest)
	}

	publishedPayload.CarrierOrigins[0].Root = nil
	publishedPayload.Cases[0] = 9
	publishedTest.PayloadPath[0] = 9
	publishedPayload, _ = m.Payload(payloadID)
	publishedTest, _ = m.CaseTest(testID)
	if publishedPayload.CarrierOrigins[0].Root != root || publishedPayload.Cases[0] != 0 || publishedTest.PayloadPath[0] != 0 {
		t.Fatalf("queries returned mutable analysis storage: payload=%#v test=%#v", publishedPayload, publishedTest)
	}
}

func TestAggregateSlotsDistinguishKnownEmptyFromMissing(t *testing.T) {
	m := newModule()
	emptyID := source.ParsedNodeID(1)
	missingID := source.ParsedNodeID(2)
	m.recordAggregateSlots(emptyID, []AggregateSlot{})
	if slots, ok := m.AggregateSlots(emptyID); !ok || len(slots) != 0 {
		t.Fatalf("known empty aggregate = %#v, %v", slots, ok)
	}
	if slots, ok := m.AggregateSlots(missingID); ok || slots != nil {
		t.Fatalf("missing aggregate = %#v, %v", slots, ok)
	}
}

func TestAggregateSlotsReturnDetachedSlice(t *testing.T) {
	m := newModule()
	id := source.ParsedNodeID(1)
	m.recordAggregateSlots(id, []AggregateSlot{{Projection: place.OriginProjection{Kind: place.OriginField, Field: "value"}}})
	slots, ok := m.AggregateSlots(id)
	if !ok || len(slots) != 1 {
		t.Fatalf("aggregate slots = %#v, %v", slots, ok)
	}
	slots[0].Projection.Field = "changed"
	again, _ := m.AggregateSlots(id)
	if again[0].Projection.Field != "value" {
		t.Fatalf("AggregateSlots returned mutable backing storage: %#v", again)
	}
}

func TestCleanupQueriesReturnDetachedSlices(t *testing.T) {
	fn := moduleid.FunctionID("test::cleanup")
	site := cfg.SiteID{Block: 1, Index: 2}
	returnID := source.ParsedNodeID(3)
	matchID := source.ParsedNodeID(4)
	first := symbols.ProjectedSymbolID(symbols.SymbolVar, "first")
	second := symbols.ProjectedSymbolID(symbols.SymbolVar, "second")
	m := newModule()
	m.cleanup = cleanupPlans{fn: {
		AfterScope:      map[cfg.SiteID][]symbols.SymbolID{site: {first, second}},
		BeforeReturn:    map[source.NodeID][]symbols.SymbolID{returnID: {first, second}},
		MatchFieldDrops: map[source.NodeID][]int{matchID: {1, 2}},
	}}

	afterScope := m.DropsAfterSite(fn, site)
	beforeReturn := m.DropsBeforeReturn(fn, returnID)
	matchFields := m.MatchFieldDrops(fn, matchID)
	afterScope[0], beforeReturn[0], matchFields[0] = second, second, 9

	if got := m.DropsAfterSite(fn, site); got[0] != first {
		t.Fatalf("scope drops shared backing storage: %v", got)
	}
	if got := m.DropsBeforeReturn(fn, returnID); got[0] != first {
		t.Fatalf("return drops shared backing storage: %v", got)
	}
	if got := m.MatchFieldDrops(fn, matchID); got[0] != 1 {
		t.Fatalf("match field drops shared backing storage: %v", got)
	}
}
