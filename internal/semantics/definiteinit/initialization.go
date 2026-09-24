package definiteinit

import (
	"compiler/internal/diagnostics"
	graphcore "compiler/internal/graph"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/symbols"
	"compiler/internal/source"
)

type state map[symbols.SymbolID]struct{}

type functionResult struct {
	In map[cfg.SiteID]state
}

// Check diagnoses reads not initialized on every reachable CFG predecessor.
//
// It consumes published effects and never inspects syntax, so a new construct
// that defines, writes, or reads a binding needs no case here.
func Check(graphs *cfg.Module, effects effect.Result, diag *diagnostics.DiagnosticBag) {
	if graphs == nil {
		return
	}
	for _, graph := range graphs.Functions {
		if graph == nil {
			continue
		}
		analyzeFunction(graph, effects[graph.NodeID], diag)
	}
}

func analyzeFunction(graph *cfg.ControlFlowGraph, ops effect.SiteOps, diag *diagnostics.DiagnosticBag) *functionResult {
	result := &functionResult{In: make(map[cfg.SiteID]state)}
	if graph == nil || graph.Entry == nil || len(graph.Entry.Sites) == 0 {
		return result
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
	// site that receives them, so entry needs no seeded state of its own.
	entry := graph.Entry.Sites[0].ID
	result.In[entry] = make(state)
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
		out := transfer(ops[id], result.In[id])
		for _, edge := range graph.SiteEdges.OutEdges(site.ID) {
			if graph.Site(edge.To) == nil || !graph.Blocks[edge.To.Block].IsReachable {
				continue
			}
			edgeState := copyState(out)
			current, exists := result.In[edge.To]
			merged := edgeState
			if exists {
				merged = intersectState(current, edgeState)
			}
			if exists && equalState(current, merged) {
				continue
			}
			result.In[edge.To] = merged
			work.Add(edge.To)
		}
	}
	// Reporting walks declaration order rather than worklist order so diagnostics
	// are deterministic. A site absent from In was never reached.
	for _, id := range order {
		if initialized, reachable := result.In[id]; reachable {
			checkAccesses(ops[id], initialized, tracked, diag)
		}
	}
	return result
}

// trackedSymbols is the diagnosable universe: a binding this function defines.
// A symbol with no define belongs to an enclosing scope and is never reported.
func trackedSymbols(ops effect.SiteOps, order []cfg.SiteID) map[symbols.SymbolID]string {
	tracked := make(map[symbols.SymbolID]string)
	visitor := &initializationVisitor{tracked: tracked}
	for _, id := range order {
		for _, op := range ops[id] {
			effect.Visit(op, visitor)
		}
	}
	return tracked
}

// transfer applies one site's effects in evaluation order. The lattice only
// gains initialized symbols, and the join intersects, so the fixed point
// terminates.
func transfer(ops []effect.Op, in state) state {
	out := copyState(in)
	visitor := &initializationVisitor{current: out, applyState: true}
	for _, op := range ops {
		effect.Visit(op, visitor)
	}
	return out
}

// checkAccesses reports reads, borrows, and projected writes against tracked
// bindings that are not initialized at that point. It replays the site's effects
// so an initialized define or whole-root write covers a later access.
func checkAccesses(ops []effect.Op, initialized state, tracked map[symbols.SymbolID]string, diag *diagnostics.DiagnosticBag) {
	if diag == nil {
		return
	}
	visitor := &initializationVisitor{
		current: initialized, tracked: tracked, diag: diag,
		applyState: true, reportAccesses: true,
	}
	visitor.current = copyState(initialized)
	for _, op := range ops {
		effect.Visit(op, visitor)
	}
}

// initializationVisitor is the exhaustive semantic-operation contract for
// definite initialization. Adding a new effect does not compile until this
// analysis explicitly classifies it.
type initializationVisitor struct {
	current        state
	tracked        map[symbols.SymbolID]string
	diag           *diagnostics.DiagnosticBag
	applyState     bool
	reportAccesses bool
}

func (v *initializationVisitor) VisitDefine(op effect.Define) {
	if op.Symbol != nil && v.tracked != nil {
		v.tracked[op.Symbol.ID] = op.Symbol.Name
	}
	if v.applyState && op.IsInitialized && op.Symbol != nil {
		v.current[op.Symbol.ID] = struct{}{}
	}
}

func (v *initializationVisitor) VisitWrite(op effect.Write) {
	if op.Place.Root == nil {
		return
	}
	if len(op.Place.Projections) > 0 {
		if v.reportAccesses {
			reportUninitializedAccess(op.Place, op.Location, v.current, v.tracked, v.diag,
				"assign a complete value to this symbol before writing through a projection")
		}
		return
	}
	if v.applyState {
		v.current[op.Place.Root.ID] = struct{}{}
	}
}

func (v *initializationVisitor) VisitUse(op effect.Use) {
	if v.reportAccesses {
		reportUninitializedAccess(op.Place, op.Location, v.current, v.tracked, v.diag,
			"assign a value before reading this symbol")
	}
}

func (v *initializationVisitor) VisitBorrow(op effect.Borrow) {
	if v.reportAccesses {
		reportUninitializedAccess(op.Place, op.Location, v.current, v.tracked, v.diag,
			"assign a value before reading this symbol")
	}
}

func (*initializationVisitor) VisitIterate(effect.Iterate)     {}
func (*initializationVisitor) VisitDiscard(effect.Discard)     {}
func (*initializationVisitor) VisitCallBegin(effect.CallBegin) {}
func (*initializationVisitor) VisitCallEnd(effect.CallEnd)     {}

func reportUninitializedAccess(at effect.Place, location *source.Location, current state, tracked map[symbols.SymbolID]string, diag *diagnostics.DiagnosticBag, help string) {
	if at.Root == nil {
		return
	}
	name, local := tracked[at.Root.ID]
	if !local {
		return
	}
	if _, present := current[at.Root.ID]; present {
		return
	}
	if name == "" {
		name = at.Root.Name
	}
	name = ir.StripSymbolInstance(name)
	msg := "symbol `" + name + "` used before it's initialized"
	diag.Add(diagnostics.NewError(msg).
		WithCode(diagnostics.ErrUninitializedVariable).
		WithPrimaryLabel(location, msg).
		WithHelp(help))
}

func copyState(current state) state {
	copied := make(state, len(current))
	for symbol := range current {
		copied[symbol] = struct{}{}
	}
	return copied
}

func intersectState(left, right state) state {
	intersection := make(state)
	for symbol := range left {
		if _, present := right[symbol]; present {
			intersection[symbol] = struct{}{}
		}
	}
	return intersection
}

func equalState(left, right state) bool {
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
