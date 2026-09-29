package analysis

import (
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
	"slices"
)

type EffectStreamsForTest = effectStreams
type EffectSiteOpsForTest = effectSiteOps

type OpForTest = effectOp
type PlaceForTest = effectPlace
type DefineForTest = effectDefine
type WriteForTest = effectWrite
type UseForTest = effectUse
type BorrowForTest = effectBorrow
type IterateForTest = effectIterate
type DiscardForTest = effectDiscard
type CallBeginForTest = effectCallBegin
type CallEndForTest = effectCallEnd

func BuildEffectsForTest(source *thir.Module, graphs *cfg.Module) EffectStreamsForTest {
	return buildEffects(source, graphs)
}

func ValidateEffectsForTest(streams EffectStreamsForTest, graphs *cfg.Module, source *thir.Module) error {
	return streams.validate(graphs, source)
}

type LoanForTest struct {
	Path      []place.OriginProjection
	Node      source.NodeID
	Origins   []place.Origin
	IsMutable bool
	Site      ir.SourceInfo
}

type OwnershipSiteForTest struct {
	ID          cfg.SiteID
	Kind        cfg.SiteKind
	NodeID      source.NodeID
	BlockNodeID source.NodeID
}

type OwnershipInspectionForTest struct {
	FunctionScope *symbols.Scope
	InReferences  map[cfg.SiteID]map[*symbols.Symbol][]LoanForTest
	SymbolLiveIn  map[cfg.SiteID]map[*symbols.Symbol]ir.SourceInfo
	SymbolLiveOut map[cfg.SiteID]map[*symbols.Symbol]ir.SourceInfo
	SiteOps       map[cfg.SiteID][]effectOp
	Sites         []OwnershipSiteForTest
	inner         *analyzer
}

func InspectOwnershipForTest(
	diag *diagnostics.DiagnosticBag,
	source *thir.Module,
	graphs *cfg.Module,
	facts *Module,
	moduleScope, functionScope *symbols.Scope,
	symbolIndex *symbols.Index,
	fnID moduleid.FunctionID,
) *OwnershipInspectionForTest {
	if source == nil || graphs == nil || facts == nil || functionScope == nil {
		return nil
	}
	graph := graphs.FunctionByID(fnID)
	fn := source.FunctionByID(fnID)
	if graph == nil || fn == nil {
		return nil
	}
	effects := buildEffects(source, graphs)
	plan := facts.cleanup[fnID]
	if plan == nil {
		return nil
	}
	input := ownershipInput{Source: source, CFG: graphs, Analysis: facts, Ops: effects, Scope: moduleScope, SymbolIndex: symbolIndex}
	sites, order := indexSites(input, graph, functionScope)
	a := &analyzer{
		diagnostics: diag, input: input, graph: graph, sites: sites, order: order,
		effects: effects[fnID], cleanup: plan, function: fn, functionScope: functionScope,
		reportedJoin: make(map[cfg.SiteID]bool),
	}
	a.run()
	return snapshotOwnershipInspectionForTest(a)
}

func snapshotOwnershipInspectionForTest(a *analyzer) *OwnershipInspectionForTest {
	if a == nil {
		return nil
	}
	out := &OwnershipInspectionForTest{
		FunctionScope: a.functionScope,
		InReferences:  make(map[cfg.SiteID]map[*symbols.Symbol][]LoanForTest),
		SymbolLiveIn:  a.symbolLiveIn,
		SymbolLiveOut: a.symbolLiveOut,
		SiteOps:       make(map[cfg.SiteID][]effectOp, len(a.effects)),
		inner:         a,
	}
	for siteID, ops := range a.effects {
		out.SiteOps[siteID] = slices.Clone(ops)
	}
	for id, st := range a.inStates {
		refs := make(map[*symbols.Symbol][]LoanForTest, len(st.references))
		for sym, loans := range st.references {
			values := make([]LoanForTest, 0, len(loans))
			for _, loan := range loans {
				values = append(values, LoanForTest{Path: slices.Clone(loan.path), Node: loan.id.node, Origins: place.CloneOrigins(loan.origins), IsMutable: loan.isMutable, Site: loan.site})
			}
			refs[sym] = values
		}
		out.InReferences[id] = refs
	}
	for _, node := range a.sites {
		if node == nil || node.cfgSite == nil {
			continue
		}
		entry := OwnershipSiteForTest{ID: node.cfgSite.ID, Kind: node.cfgSite.Kind, NodeID: node.cfgSite.NodeID}
		if node.block != nil {
			entry.BlockNodeID = node.block.SourceInfo().NodeID
		}
		out.Sites = append(out.Sites, entry)
	}
	return out
}

func (o *OwnershipInspectionForTest) RecomputeLivenessForTest() {
	if o == nil || o.inner == nil {
		return
	}
	o.inner.computeSymbolLiveness()
	o.SymbolLiveIn = o.inner.symbolLiveIn
	o.SymbolLiveOut = o.inner.symbolLiveOut
}

func (o *OwnershipInspectionForTest) ReferenceHoldingUseSequenceForTest(siteID cfg.SiteID) []*symbols.Symbol {
	if o == nil || o.inner == nil {
		return nil
	}
	return o.inner.symbolUseSequence(o.inner.sites[siteID], referenceHoldingSymbol)
}
