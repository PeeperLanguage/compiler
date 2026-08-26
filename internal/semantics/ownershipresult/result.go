package ownershipresult

import (
	"compiler/internal/ir"
	"compiler/internal/semantics/symbols"
)

// CleanupPlan records ownership effects at stable HIR source sites.
type CleanupPlan struct {
	AfterScope             map[ir.NodeID][]symbols.SymbolID
	BeforeReturn           map[ir.NodeID][]symbols.SymbolID
	BeforeAssign           map[ir.NodeID]struct{}
	DiscardedValue         map[ir.NodeID]struct{}
	ProjectionBase         map[ir.NodeID]struct{}
	MatchCarrierMoves      map[ir.NodeID]symbols.SymbolID
	MatchFieldDrops        map[ir.NodeID][]int
	MatchWholePayloadDrops map[ir.NodeID]struct{}
}

// Result stores ownership output by stable HIR function identity.
type Result map[ir.NodeID]*CleanupPlan
