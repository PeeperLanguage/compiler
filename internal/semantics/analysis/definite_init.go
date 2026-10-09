package analysis

import (
	"compiler/internal/diagnostics"
	graphcore "compiler/internal/graph"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

type initState map[symbols.SymbolID]struct{}

// checkInitialization diagnoses reads not initialized on every reachable CFG predecessor.
//
// It consumes the derived effect stream and never inspects syntax, so a new construct
// that defines, writes, or reads a binding needs no case here.
func checkInitialization(graphs *cfg.Module, effects effectStreams, diag *diagnostics.DiagnosticBag) {
	if graphs == nil {
		return
	}
	for _, graph := range graphs.Functions {
		if graph == nil {
			continue
		}
		analyzeFunction(graph, effects[graph.FunctionID], diag)
	}
}

func analyzeFunction(graph *cfg.ControlFlowGraph, ops effectSiteOps, diag *diagnostics.DiagnosticBag) map[cfg.SiteID]initState {
	inStates := make(map[cfg.SiteID]initState)
	if graph == nil || graph.Entry == nil || len(graph.Entry.Sites) == 0 {
		return inStates
	}
	order := make([]cfg.SiteID, 0)
	for _, block := range graph.Blocks {
		if block == nil || !block.IsReachable {
			continue
		}
		for _, site := range block.Sites {
			if site != nil {
				order = append(order, site.ID)
			}
		}
	}
	tracked := trackedSymbols(ops, order)

	// Parameters and match payload bindings arrive as initialized defines at the
	// site that receives them, so entry needs no seeded initState of its own.
	entry := graph.Entry.Sites[0].ID
	inStates[entry] = make(initState)
	work := graphcore.NewWorklist(entry)
	for {
		id, pending := work.Next()
		if !pending {
			break
		}
		site := graph.Site(id)
		if site == nil || !graph.Blocks[id.Block].IsReachable {
			continue
		}
		out := transfer(ops[id], inStates[id])
		for _, edge := range graph.SiteEdges.OutEdges(site.ID) {
			if graph.Site(edge.To) == nil || !graph.Blocks[edge.To.Block].IsReachable {
				continue
			}
			edgeState := copyInitState(out)
			current, exists := inStates[edge.To]
			merged := edgeState
			if exists {
				merged = intersectState(current, edgeState)
			}
			if exists && areInitStatesEqual(current, merged) {
				continue
			}
			inStates[edge.To] = merged
			work.Add(edge.To)
		}
	}
	// Reporting walks declaration order rather than worklist order so diagnostics
	// are deterministic. A site absent from inStates was never reached.
	for _, id := range order {
		if initialized, isReachable := inStates[id]; isReachable {
			checkAccesses(ops[id], initialized, tracked, diag)
		}
	}
	return inStates
}

// trackedSymbols is the diagnosable universe: a binding this function defines.
// A symbol with no define belongs to an enclosing scope and is never reported.
func trackedSymbols(ops effectSiteOps, order []cfg.SiteID) map[symbols.SymbolID]string {
	tracked := make(map[symbols.SymbolID]string)
	visitor := &initializationVisitor{tracked: tracked}
	for _, id := range order {
		for _, op := range ops[id] {
			visitEffect(op, visitor)
		}
	}
	return tracked
}

// transfer applies one site's effects in evaluation order. The lattice only
// gains initialized symbols, and the join intersects, so the fixed point
// terminates.
func transfer(ops []effectOp, in initState) initState {
	out := copyInitState(in)
	visitor := &initializationVisitor{current: out, shouldApplyState: true}
	for _, op := range ops {
		visitEffect(op, visitor)
	}
	return out
}

// checkAccesses reports reads, borrows, and projected writes against tracked
// bindings that are not initialized at that point. It replays the site's effects
// so an initialized define or whole-root write covers a later access.
func checkAccesses(ops []effectOp, initialized initState, tracked map[symbols.SymbolID]string, diag *diagnostics.DiagnosticBag) {
	if diag == nil {
		return
	}
	visitor := &initializationVisitor{
		current: initialized, tracked: tracked, diag: diag,
		shouldApplyState: true, shouldReportAccesses: true,
	}
	visitor.current = copyInitState(initialized)
	for _, op := range ops {
		visitEffect(op, visitor)
	}
}

// initializationVisitor is the exhaustive semantic-operation contract for
// definite initialization. Adding a new effect does not compile until this
// analysis explicitly classifies it.
type initializationVisitor struct {
	current              initState
	tracked              map[symbols.SymbolID]string
	diag                 *diagnostics.DiagnosticBag
	shouldApplyState     bool
	shouldReportAccesses bool
}

func (v *initializationVisitor) visitDefine(op effectDefine) {
	if op.Symbol != nil && v.tracked != nil {
		v.tracked[op.Symbol.ID] = op.Symbol.Name
	}
	if v.shouldApplyState && op.IsInitialized && op.Symbol != nil {
		v.current[op.Symbol.ID] = struct{}{}
	}
}

func (v *initializationVisitor) visitWrite(op effectWrite) {
	if op.Place.Root == nil {
		return
	}
	if len(op.Place.Projections) > 0 {
		if v.shouldReportAccesses {
			reportUninitializedAccess(op.Place, op.Location, v.current, v.tracked, v.diag,
				"assign a complete value to this symbol before writing through a projection")
		}
		return
	}
	if v.shouldApplyState {
		v.current[op.Place.Root.ID] = struct{}{}
	}
}

func (v *initializationVisitor) visitUse(op effectUse) {
	if v.shouldReportAccesses {
		reportUninitializedAccess(op.Place, op.Location, v.current, v.tracked, v.diag,
			"assign a value before reading this symbol")
	}
}

func (v *initializationVisitor) visitBorrow(op effectBorrow) {
	if v.shouldReportAccesses {
		reportUninitializedAccess(op.Place, op.Location, v.current, v.tracked, v.diag,
			"assign a value before reading this symbol")
	}
}

func (*initializationVisitor) visitIterate(effectIterate)     {}
func (*initializationVisitor) visitDiscard(effectDiscard)     {}
func (*initializationVisitor) visitCallBegin(effectCallBegin) {}
func (*initializationVisitor) visitCallEnd(effectCallEnd)     {}

func reportUninitializedAccess(at effectPlace, location *source.Location, current initState, tracked map[symbols.SymbolID]string, diag *diagnostics.DiagnosticBag, help string) {
	if at.Root == nil {
		return
	}
	name, isLocal := tracked[at.Root.ID]
	if !isLocal {
		return
	}
	if _, present := current[at.Root.ID]; present {
		return
	}
	if name == "" {
		name = at.Root.Name
	}
	msg := "symbol `" + name + "` used before it's initialized"
	diag.Add(diagnostics.NewError(msg).
		WithCode(diagnostics.ErrUninitializedVariable).
		WithPrimaryLabel(location, msg).
		Help(help))
}

func copyInitState(current initState) initState {
	copied := make(initState, len(current))
	for symbol := range current {
		copied[symbol] = struct{}{}
	}
	return copied
}

func intersectState(left, right initState) initState {
	intersection := make(initState)
	for symbol := range left {
		if _, present := right[symbol]; present {
			intersection[symbol] = struct{}{}
		}
	}
	return intersection
}

func areInitStatesEqual(left, right initState) bool {
	if len(left) != len(right) {
		return false
	}
	for symbol := range left {
		if _, present := right[symbol]; !present {
			return false
		}
	}
	return true
}
