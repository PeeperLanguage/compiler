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

type PresenceFact struct {
	CarrierOrigins []place.Origin
	Depth          int
	Dependencies   []symbols.SymbolID
}

type Facts struct {
	Presence          []PresenceFact
	ReferenceOrigins  map[symbols.SymbolID][]place.Origin
	RawPointerOrigins map[symbols.SymbolID][]place.Origin
}

type PayloadAccess struct {
	CarrierOrigins []place.Origin
	Depth          int
	Direct         bool
}

type OptionalTest struct {
	SubjectID       ast.NodeID
	PresentWhenTrue bool
	Depth           int
}

type Result struct {
	SiteFacts              map[ir.NodeID]map[cfg.SiteID]Facts
	ExprTypes              map[ast.NodeID]typeinfo.Type
	Payloads               map[ast.NodeID]PayloadAccess
	OptionalTests          map[ast.NodeID]OptionalTest
	ResolvedStorageOrigins map[ast.NodeID][]place.Origin
	ResolvedValueOrigins   map[ast.NodeID][]place.Origin
}
