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

type Facts struct {
	Variants          []VariantFact
	ReferenceOrigins  map[symbols.SymbolID][]place.Origin
	RawPointerOrigins map[symbols.SymbolID][]place.Origin
}

type PayloadAccess struct {
	CarrierOrigins []place.Origin
	Cases          []int
	Direct         bool
}

// OptionalTest is base typechecker evidence for source `none` comparisons.
// Flow converts it into case-based VariantTest evidence.
type OptionalTest struct {
	SubjectID       ast.NodeID
	PresentWhenTrue bool
}

type VariantTest struct {
	SubjectID    ast.NodeID
	Case         int
	CaseWhenTrue bool
	CaseCount    int
	PayloadPath  []int
}

type Result struct {
	SiteFacts              map[ir.NodeID]map[cfg.SiteID]Facts
	ExprTypes              map[ast.NodeID]typeinfo.Type
	Payloads               map[ast.NodeID]PayloadAccess
	VariantTests           map[ast.NodeID]VariantTest
	ResolvedStorageOrigins map[ast.NodeID][]place.Origin
	ResolvedValueOrigins   map[ast.NodeID][]place.Origin
}
