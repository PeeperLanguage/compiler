package analysis

import (
	"compiler/internal/source"

	"compiler/internal/ir/cfg"

	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// Input supplies the immutable artifacts required for post-CFG semantic analysis.
type Input struct {
	Source      *thir.Module
	CFG         *cfg.Module
	Scope       *symbols.Scope
	SymbolIndex *symbols.Index
}

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
	conversions   map[source.NodeID]typeinfo.Conversion
	payloads      map[source.NodeID]PayloadAccess
	caseTests     map[source.NodeID]CaseTest
	variantFields map[source.NodeID]VariantFieldAccess
	origins       map[source.NodeID]OriginResolution
	aggregates    map[source.NodeID][]AggregateSlot
}

// Module owns durable post-CFG semantic evidence for one generation. Backing maps
// stay private so consumers query semantic facts rather than storage layout.
type Module struct {
	expressions expressionEvidence
	cleanup     cleanupPlans
}

func newModule() *Module {
	return &Module{expressions: expressionEvidence{
		types:         make(map[source.NodeID]typeinfo.Type),
		conversions:   make(map[source.NodeID]typeinfo.Conversion),
		payloads:      make(map[source.NodeID]PayloadAccess),
		caseTests:     make(map[source.NodeID]CaseTest),
		variantFields: make(map[source.NodeID]VariantFieldAccess),
		origins:       make(map[source.NodeID]OriginResolution),
		aggregates:    make(map[source.NodeID][]AggregateSlot),
	}}
}

func (r *Module) recordExprType(id source.NodeID, typ typeinfo.Type) {
	if r != nil && id.IsValid() && typ != nil {
		r.expressions.types[id] = typ
	}
}

func (r *Module) ExprType(id source.NodeID) typeinfo.Type {
	if r == nil || !id.IsValid() {
		return nil
	}
	return r.expressions.types[id]
}

// ImplicitConversion supplies conversions whose destination was unknown until
// flow resolved an enum payload field. Base conversions remain owned by THIR.
func (r *Module) ImplicitConversion(id source.NodeID) (typeinfo.Conversion, bool) {
	if r == nil || !id.IsValid() {
		return typeinfo.Conversion{}, false
	}
	conversion, ok := r.expressions.conversions[id]
	return conversion, ok
}

func (r *Module) recordPayload(id source.NodeID, payload PayloadAccess) {
	if r != nil && id.IsValid() {
		r.expressions.payloads[id] = clonePayload(payload)
	}
}

func (r *Module) Payload(id source.NodeID) (PayloadAccess, bool) {
	if r == nil || !id.IsValid() {
		return PayloadAccess{}, false
	}
	payload, ok := r.expressions.payloads[id]
	return clonePayload(payload), ok
}

func clonePayload(payload PayloadAccess) PayloadAccess {
	payload.CarrierOrigins = place.CloneOrigins(payload.CarrierOrigins)
	payload.Cases = append([]int(nil), payload.Cases...)
	return payload
}

func (r *Module) forgetPayload(id source.NodeID) {
	if r != nil {
		delete(r.expressions.payloads, id)
	}
}

func (r *Module) recordCaseTest(id source.NodeID, test CaseTest) {
	if r != nil && id.IsValid() {
		r.expressions.caseTests[id] = cloneCaseTest(test)
	}
}

func (r *Module) CaseTest(id source.NodeID) (CaseTest, bool) {
	if r == nil || !id.IsValid() {
		return CaseTest{}, false
	}
	test, ok := r.expressions.caseTests[id]
	return cloneCaseTest(test), ok
}

func cloneCaseTest(test CaseTest) CaseTest {
	test.PayloadPath = append([]int(nil), test.PayloadPath...)
	return test
}

func (r *Module) recordVariantField(id source.NodeID, field VariantFieldAccess) {
	if r != nil && id.IsValid() {
		r.expressions.variantFields[id] = field
	}
}

func (r *Module) VariantField(id source.NodeID) (VariantFieldAccess, bool) {
	if r == nil || !id.IsValid() {
		return VariantFieldAccess{}, false
	}
	field, ok := r.expressions.variantFields[id]
	return field, ok
}

func (r *Module) recordOrigins(id source.NodeID, storage, value []place.Origin) {
	if r == nil || !id.IsValid() {
		return
	}
	r.expressions.origins[id] = OriginResolution{
		Storage: place.CloneOrigins(storage),
		Value:   place.CloneOrigins(value),
	}
}

func (r *Module) mergeOrigins(id source.NodeID, storage, value []place.Origin) {
	if r == nil || !id.IsValid() {
		return
	}
	current := r.expressions.origins[id]
	current.Storage = place.MergeOrigins(current.Storage, storage)
	current.Value = place.MergeOrigins(current.Value, value)
	r.expressions.origins[id] = current
}

func (r *Module) Origins(id source.NodeID) (OriginResolution, bool) {
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

func (r *Module) StorageOrigins(id source.NodeID) []place.Origin {
	origins, _ := r.Origins(id)
	return origins.Storage
}

func (r *Module) ValueOrigins(id source.NodeID) []place.Origin {
	origins, _ := r.Origins(id)
	return origins.Value
}

// recordAggregateSlots publishes the direct slot decomposition Flow used when
// storing an aggregate value. Recording an empty slice is meaningful: the
// expression is an aggregate with no direct child slots.
func (r *Module) recordAggregateSlots(id source.NodeID, slots []AggregateSlot) {
	if r == nil || !id.IsValid() {
		return
	}
	r.expressions.aggregates[id] = append([]AggregateSlot(nil), slots...)
}

// AggregateSlots returns the direct slot decomposition published for an
// aggregate expression. The bool distinguishes a known empty aggregate from
// an expression that has no aggregate evidence.
func (r *Module) AggregateSlots(id source.NodeID) ([]AggregateSlot, bool) {
	if r == nil || !id.IsValid() {
		return nil, false
	}
	slots, ok := r.expressions.aggregates[id]
	if !ok {
		return nil, false
	}
	return append([]AggregateSlot(nil), slots...), true
}

func (m *Module) DropsAfterSite(fn moduleid.FunctionID, site cfg.SiteID) []symbols.SymbolID {
	if m == nil || m.cleanup == nil || m.cleanup[fn] == nil {
		return nil
	}
	return append([]symbols.SymbolID(nil), m.cleanup[fn].AfterScope[site]...)
}

func (m *Module) DropsBeforeReturn(fn moduleid.FunctionID, id source.NodeID) []symbols.SymbolID {
	if m == nil || m.cleanup == nil || m.cleanup[fn] == nil {
		return nil
	}
	return append([]symbols.SymbolID(nil), m.cleanup[fn].BeforeReturn[id]...)
}

func (m *Module) DropsBeforeAssign(fn moduleid.FunctionID, id source.NodeID) bool {
	if m == nil || m.cleanup == nil || m.cleanup[fn] == nil {
		return false
	}
	_, ok := m.cleanup[fn].BeforeAssign[id]
	return ok
}

func (m *Module) DiscardedValue(fn moduleid.FunctionID, id source.NodeID) bool {
	if m == nil || m.cleanup == nil || m.cleanup[fn] == nil {
		return false
	}
	_, ok := m.cleanup[fn].DiscardedValue[id]
	return ok
}

func (m *Module) ProjectionBase(fn moduleid.FunctionID, id source.NodeID) bool {
	if m == nil || m.cleanup == nil || m.cleanup[fn] == nil {
		return false
	}
	_, ok := m.cleanup[fn].ProjectionBase[id]
	return ok
}

func (m *Module) MatchFieldDrops(fn moduleid.FunctionID, id source.NodeID) []int {
	if m == nil || m.cleanup == nil || m.cleanup[fn] == nil {
		return nil
	}
	return append([]int(nil), m.cleanup[fn].MatchFieldDrops[id]...)
}

func (m *Module) MatchWholePayloadDrop(fn moduleid.FunctionID, id source.NodeID) bool {
	if m == nil || m.cleanup == nil || m.cleanup[fn] == nil {
		return false
	}
	_, ok := m.cleanup[fn].MatchWholePayloadDrops[id]
	return ok
}
