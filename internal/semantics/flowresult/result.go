// Package flowresult defines semantic evidence produced by flow typing and
// consumed by ownership, lowering, and language tooling.
package flowresult

import (
	"compiler/internal/source"

	"compiler/internal/ir/thir"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/typeinfo"
)

type PayloadAccess struct {
	CarrierOrigins []place.Origin
	Cases          []int
	IsDirect       bool
}

// AppliesTo distinguishes payload layers of an expression from projections
// used to reach that expression through an enclosing variant payload.
func (p PayloadAccess) AppliesTo(storage []place.Origin) bool {
	return len(p.Cases) > 0 && place.AreSameOrigins(p.CarrierOrigins, storage)
}

type CaseTest struct {
	SubjectID       source.NodeID
	Case            int
	MatchesWhenTrue bool
	CaseCount       int
	Family          typeinfo.VariantFamily
	PayloadPath     []int
}

type VariantFieldAccess struct {
	Carrier source.NodeID
	Case    int
	Payload *typeinfo.StructType
	Field   int
	Type    typeinfo.Type
}

// OriginResolution is one flow-resolved place result. Storage and value
// origins are published together because they share syntax identity, lifetime,
// and invalidation rules.
type OriginResolution struct {
	Storage []place.Origin
	Value   []place.Origin
}

// AggregateSlot describes one direct value slot of an aggregate expression.
// Projection is relative to aggregate storage; ValueExpr carries the typed
// expression whose reference provenance populates that slot.
type AggregateSlot struct {
	ValueExpr  thir.Expr
	Projection place.OriginProjection
}

type expressionEvidence struct {
	types         map[source.NodeID]typeinfo.Type
	payloads      map[source.NodeID]PayloadAccess
	caseTests     map[source.NodeID]CaseTest
	variantFields map[source.NodeID]VariantFieldAccess
	origins       map[source.NodeID]OriginResolution
	aggregates    map[source.NodeID][]AggregateSlot
}

// Result owns path-sensitive evidence for one flow generation. Backing maps
// stay private so consumers query semantic facts rather than storage layout.
type Result struct {
	expressions expressionEvidence
}

func New() *Result {
	return &Result{expressions: expressionEvidence{
		types:         make(map[source.NodeID]typeinfo.Type),
		payloads:      make(map[source.NodeID]PayloadAccess),
		caseTests:     make(map[source.NodeID]CaseTest),
		variantFields: make(map[source.NodeID]VariantFieldAccess),
		origins:       make(map[source.NodeID]OriginResolution),
		aggregates:    make(map[source.NodeID][]AggregateSlot),
	}}
}

func (r *Result) RecordExprType(id source.NodeID, typ typeinfo.Type) {
	if r != nil && id.IsValid() && typ != nil {
		r.expressions.types[id] = typ
	}
}

func (r *Result) ExprType(id source.NodeID) typeinfo.Type {
	if r == nil || !id.IsValid() {
		return nil
	}
	return r.expressions.types[id]
}

func (r *Result) RecordPayload(id source.NodeID, payload PayloadAccess) {
	if r != nil && id.IsValid() {
		r.expressions.payloads[id] = payload
	}
}

func (r *Result) Payload(id source.NodeID) (PayloadAccess, bool) {
	if r == nil || !id.IsValid() {
		return PayloadAccess{}, false
	}
	payload, ok := r.expressions.payloads[id]
	return payload, ok
}

func (r *Result) ForgetPayload(id source.NodeID) {
	if r != nil {
		delete(r.expressions.payloads, id)
	}
}

func (r *Result) RecordCaseTest(id source.NodeID, test CaseTest) {
	if r != nil && id.IsValid() {
		r.expressions.caseTests[id] = test
	}
}

func (r *Result) CaseTest(id source.NodeID) (CaseTest, bool) {
	if r == nil || !id.IsValid() {
		return CaseTest{}, false
	}
	test, ok := r.expressions.caseTests[id]
	return test, ok
}

func (r *Result) RecordVariantField(id source.NodeID, field VariantFieldAccess) {
	if r != nil && id.IsValid() {
		r.expressions.variantFields[id] = field
	}
}

func (r *Result) VariantField(id source.NodeID) (VariantFieldAccess, bool) {
	if r == nil || !id.IsValid() {
		return VariantFieldAccess{}, false
	}
	field, ok := r.expressions.variantFields[id]
	return field, ok
}

func (r *Result) RecordOrigins(id source.NodeID, storage, value []place.Origin) {
	if r == nil || !id.IsValid() {
		return
	}
	r.expressions.origins[id] = OriginResolution{
		Storage: place.CloneOrigins(storage),
		Value:   place.CloneOrigins(value),
	}
}

func (r *Result) MergeOrigins(id source.NodeID, storage, value []place.Origin) {
	if r == nil || !id.IsValid() {
		return
	}
	current := r.expressions.origins[id]
	current.Storage = place.MergeOrigins(current.Storage, storage)
	current.Value = place.MergeOrigins(current.Value, value)
	r.expressions.origins[id] = current
}

func (r *Result) Origins(id source.NodeID) (OriginResolution, bool) {
	if r == nil || !id.IsValid() {
		return OriginResolution{}, false
	}
	origins, ok := r.expressions.origins[id]
	if !ok {
		return OriginResolution{}, false
	}
	return OriginResolution{
		Storage: place.CloneOrigins(origins.Storage),
		Value:   place.CloneOrigins(origins.Value),
	}, true
}

func (r *Result) StorageOrigins(id source.NodeID) []place.Origin {
	origins, _ := r.Origins(id)
	return origins.Storage
}

func (r *Result) ValueOrigins(id source.NodeID) []place.Origin {
	origins, _ := r.Origins(id)
	return origins.Value
}

// RecordAggregateSlots publishes the direct slot decomposition Flow used when
// storing an aggregate value. Recording an empty slice is meaningful: the
// expression is an aggregate with no direct child slots.
func (r *Result) RecordAggregateSlots(id source.NodeID, slots []AggregateSlot) {
	if r == nil || !id.IsValid() {
		return
	}
	r.expressions.aggregates[id] = append([]AggregateSlot(nil), slots...)
}

// AggregateSlots returns the direct slot decomposition published for an
// aggregate expression. The bool distinguishes a known empty aggregate from
// an expression that has no aggregate evidence.
func (r *Result) AggregateSlots(id source.NodeID) ([]AggregateSlot, bool) {
	if r == nil || !id.IsValid() {
		return nil, false
	}
	slots, ok := r.expressions.aggregates[id]
	if !ok {
		return nil, false
	}
	return append([]AggregateSlot(nil), slots...), true
}
