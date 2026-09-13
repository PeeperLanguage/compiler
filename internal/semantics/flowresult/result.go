// Package flowresult defines semantic evidence produced by flow typing and
// consumed by ownership, lowering, and language tooling.
package flowresult

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

type PayloadAccess struct {
	CarrierOrigins []place.Origin
	Cases          []int
	Direct         bool
}

// AppliesTo distinguishes payload layers of an expression from projections
// used to reach that expression through an enclosing variant payload.
func (p PayloadAccess) AppliesTo(storage []place.Origin) bool {
	return len(p.Cases) > 0 && place.SameOrigins(p.CarrierOrigins, storage)
}

type CaseTest struct {
	typecheckresult.CaseTest
	PayloadPath []int
}

type VariantFieldAccess struct {
	Carrier ast.NodeID
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

type expressionEvidence struct {
	types         map[ast.NodeID]typeinfo.Type
	payloads      map[ast.NodeID]PayloadAccess
	caseTests     map[ast.NodeID]CaseTest
	variantFields map[ast.NodeID]VariantFieldAccess
	origins       map[ast.NodeID]OriginResolution
}

// Result owns path-sensitive evidence for one flow generation. Backing maps
// stay private so consumers query semantic facts rather than storage layout.
type Result struct {
	expressions expressionEvidence
}

func New() *Result {
	return &Result{expressions: expressionEvidence{
		types:         make(map[ast.NodeID]typeinfo.Type),
		payloads:      make(map[ast.NodeID]PayloadAccess),
		caseTests:     make(map[ast.NodeID]CaseTest),
		variantFields: make(map[ast.NodeID]VariantFieldAccess),
		origins:       make(map[ast.NodeID]OriginResolution),
	}}
}

func (r *Result) RecordExprType(id ast.NodeID, typ typeinfo.Type) {
	if r != nil && id != 0 && typ != nil {
		r.expressions.types[id] = typ
	}
}

func (r *Result) ExprType(id ast.NodeID) typeinfo.Type {
	if r == nil || id == 0 {
		return nil
	}
	return r.expressions.types[id]
}

func (r *Result) ForgetExprType(id ast.NodeID) {
	if r != nil {
		delete(r.expressions.types, id)
	}
}

func (r *Result) RecordPayload(id ast.NodeID, payload PayloadAccess) {
	if r != nil && id != 0 {
		r.expressions.payloads[id] = payload
	}
}

func (r *Result) Payload(id ast.NodeID) (PayloadAccess, bool) {
	if r == nil || id == 0 {
		return PayloadAccess{}, false
	}
	payload, ok := r.expressions.payloads[id]
	return payload, ok
}

func (r *Result) ForgetPayload(id ast.NodeID) {
	if r != nil {
		delete(r.expressions.payloads, id)
	}
}

func (r *Result) RecordCaseTest(id ast.NodeID, test CaseTest) {
	if r != nil && id != 0 {
		r.expressions.caseTests[id] = test
	}
}

func (r *Result) CaseTest(id ast.NodeID) (CaseTest, bool) {
	if r == nil || id == 0 {
		return CaseTest{}, false
	}
	test, ok := r.expressions.caseTests[id]
	return test, ok
}

func (r *Result) RecordVariantField(id ast.NodeID, field VariantFieldAccess) {
	if r != nil && id != 0 {
		r.expressions.variantFields[id] = field
	}
}

func (r *Result) VariantField(id ast.NodeID) (VariantFieldAccess, bool) {
	if r == nil || id == 0 {
		return VariantFieldAccess{}, false
	}
	field, ok := r.expressions.variantFields[id]
	return field, ok
}

func (r *Result) RecordOrigins(id ast.NodeID, storage, value []place.Origin) {
	if r == nil || id == 0 {
		return
	}
	r.expressions.origins[id] = OriginResolution{
		Storage: place.CloneOrigins(storage),
		Value:   place.CloneOrigins(value),
	}
}

func (r *Result) MergeOrigins(id ast.NodeID, storage, value []place.Origin) {
	if r == nil || id == 0 {
		return
	}
	current := r.expressions.origins[id]
	current.Storage = place.MergeOrigins(current.Storage, storage)
	current.Value = place.MergeOrigins(current.Value, value)
	r.expressions.origins[id] = current
}

func (r *Result) Origins(id ast.NodeID) (OriginResolution, bool) {
	if r == nil || id == 0 {
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

func (r *Result) StorageOrigins(id ast.NodeID) []place.Origin {
	origins, _ := r.Origins(id)
	return origins.Storage
}

func (r *Result) ValueOrigins(id ast.NodeID) []place.Origin {
	origins, _ := r.Origins(id)
	return origins.Value
}
