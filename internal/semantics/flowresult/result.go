// Package flowresult defines semantic evidence produced by flow typing and
// consumed by ownership, lowering, and language tooling.
package flowresult

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

type VariantFact struct {
	CarrierOrigins []place.Origin
	Cases          []int
	CaseCount      int
	Dependencies   []symbols.SymbolID
}

type OriginFact struct {
	StorageOrigins []place.Origin
	ValueOrigins   []place.Origin
}

type Facts struct {
	Variants          []VariantFact
	ReferenceOrigins  []OriginFact
	RawPointerOrigins []OriginFact
}

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
	SubjectID    ast.NodeID
	Case         int
	CaseWhenTrue bool
	CaseCount    int
	PayloadPath  []int
	Family       typeinfo.VariantFamily
}

type VariantFieldAccess struct {
	Carrier ast.NodeID
	Case    int
	Payload *typeinfo.StructType
	Field   int
	Type    typeinfo.Type
}

// Match is typechecker-owned case and binding evidence consumed by CFG and
// later semantic phases without resolving source paths again.
type Match struct {
	SubjectID ast.NodeID
	EnumType  typeinfo.Type
	Cases     []typeinfo.VariantCase
	Arms      []MatchArm
}

type MatchArm struct {
	ArmID   ast.NodeID
	BodyID  ast.NodeID
	Case    int
	Payload typeinfo.Type
	Fields  []MatchField
}

type MatchField struct {
	Field        int
	WholePayload bool
	Type         typeinfo.Type
	Binding      *symbols.Symbol
	Discard      bool
}

// Arm returns resolved evidence for one case-labelled CFG edge.
func (m Match) Arm(caseIndex int) (MatchArm, bool) {
	for _, arm := range m.Arms {
		if arm.Case == caseIndex {
			return arm, true
		}
	}
	return MatchArm{}, false
}

type Result struct {
	SiteFacts              map[ir.NodeID]map[cfg.SiteID]Facts
	ExprTypes              map[ast.NodeID]typeinfo.Type
	Payloads               map[ast.NodeID]PayloadAccess
	CaseTests              map[ast.NodeID]CaseTest
	VariantFields          map[ast.NodeID]VariantFieldAccess
	ResolvedStorageOrigins map[ast.NodeID][]place.Origin
	ResolvedValueOrigins   map[ast.NodeID][]place.Origin
}
