package ownershipresult

import (
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/symbols"
)

// CleanupPlan records ownership effects at CFG and stable HIR source sites.
//
// It is the only source of drop obligations over source values: lowering reads
// the plan and never decides a drop for itself. The two other drops in the
// pipeline are not competing policy — a source-level `free` is the programmer's
// own drop, and MIR's temporary drops destroy temporaries MIR itself
// materializes, which have no source symbol to plan against.
//
// Scope exit and return stay separate channels because the events differ. A
// scope-exit site leaves exactly one scope, and its drops emit while the block's
// sites are processed. A return leaves every enclosing scope at once, and its
// drops must emit after the returned value is computed — a value read from a
// local being unwound would otherwise be freed before it is read. Folding return
// into scope-exit sites therefore requires MIR to defer trailing site drops
// until after the terminator's value expression.
type CleanupPlan struct {
	// AfterScope drops the symbols owned by the one scope a site exits.
	AfterScope map[cfg.SiteID][]symbols.SymbolID
	// BeforeReturn drops every scope a return unwinds, after its value is
	// computed. Keyed by the return statement, which is the event, not a site.
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
