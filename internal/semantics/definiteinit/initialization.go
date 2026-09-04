package definiteinit

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/effect"
	"compiler/internal/semantics/symbols"
)

type state map[symbols.SymbolID]struct{}

type functionResult struct {
	In  map[cfg.SiteID]state
	Out map[cfg.SiteID]state
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

func analyzeFunction(graph *cfg.Graph, ops effect.SiteOps, diag *diagnostics.DiagnosticBag) *functionResult {
	result := &functionResult{In: make(map[cfg.SiteID]state), Out: make(map[cfg.SiteID]state)}
	if graph == nil || graph.Entry == nil || len(graph.Entry.Sites) == 0 {
		return result
	}
	sites, order := indexSites(graph)
	tracked := trackedSymbols(ops, order)

	// Parameters and match payload bindings arrive as initialized defines at the
	// site that receives them, so entry needs no seeded state of its own.
	entry := graph.Entry.Sites[0].ID
	result.In[entry] = make(state)
	queue := []cfg.SiteID{entry}
	queued := map[cfg.SiteID]bool{entry: true}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		queued[id] = false
		site := sites[id]
		if site == nil {
			continue
		}
		out := transfer(ops[id], result.In[id])
		result.Out[id] = out
		for _, edge := range site.Successors {
			if sites[edge.To] == nil {
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
			if !queued[edge.To] {
				queue = append(queue, edge.To)
				queued[edge.To] = true
			}
		}
	}
	// Reporting walks declaration order rather than worklist order so diagnostics
	// are deterministic. A site absent from In was never reached.
	for _, id := range order {
		if initialized, reachable := result.In[id]; reachable {
			checkReads(ops[id], initialized, tracked, diag)
		}
	}
	return result
}

func indexSites(graph *cfg.Graph) (map[cfg.SiteID]*cfg.Site, []cfg.SiteID) {
	sites := make(map[cfg.SiteID]*cfg.Site)
	order := make([]cfg.SiteID, 0)
	for _, block := range graph.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		for _, site := range block.Sites {
			if site == nil {
				continue
			}
			sites[site.ID] = site
			order = append(order, site.ID)
		}
	}
	return sites, order
}

// trackedSymbols is the diagnosable universe: a binding this function defines.
// A symbol with no define belongs to an enclosing scope and is never reported.
func trackedSymbols(ops effect.SiteOps, order []cfg.SiteID) map[symbols.SymbolID]string {
	tracked := make(map[symbols.SymbolID]string)
	for _, id := range order {
		for _, op := range ops[id] {
			define, ok := op.(effect.Define)
			if ok && define.Symbol != nil {
				tracked[define.Symbol.ID] = define.Symbol.Name
			}
		}
	}
	return tracked
}

// transfer applies one site's effects in evaluation order. The lattice only
// gains initialized symbols, and the join intersects, so the fixed point
// terminates.
func transfer(ops []effect.Op, in state) state {
	out := copyState(in)
	for _, op := range ops {
		apply(out, op)
	}
	return out
}

// checkReads reports a read of a tracked binding that is not initialized at
// that point. It replays the site's effects so a define earlier in the same
// site covers a read later in it.
func checkReads(ops []effect.Op, initialized state, tracked map[symbols.SymbolID]string, diag *diagnostics.DiagnosticBag) {
	if diag == nil {
		return
	}
	current := copyState(initialized)
	for _, op := range ops {
		if use, ok := op.(effect.Use); ok {
			reportUninitializedRead(use, current, tracked, diag)
		}
		apply(current, op)
	}
}

// apply is the single place an effect changes initialization state. A new
// operation kind fails here by name rather than being silently ignored.
func apply(current state, op effect.Op) {
	switch op := op.(type) {
	case effect.Define:
		if op.Initialized && op.Symbol != nil {
			current[op.Symbol.ID] = struct{}{}
		}
	case effect.Write:
		if op.Symbol != nil {
			current[op.Symbol.ID] = struct{}{}
		}
	case effect.Use:
		// A read leaves initialization state unchanged.
	default:
		panic(fmt.Sprintf("definiteinit: unhandled effect %T", op))
	}
}

func reportUninitializedRead(use effect.Use, current state, tracked map[symbols.SymbolID]string, diag *diagnostics.DiagnosticBag) {
	if use.Symbol == nil {
		return
	}
	name, local := tracked[use.Symbol.ID]
	if !local {
		return
	}
	if _, present := current[use.Symbol.ID]; present {
		return
	}
	if name == "" {
		name = use.Symbol.Name
	}
	name = ir.StripSymbolInstance(name)
	msg := "symbol `" + name + "` used before it's initialized"
	diag.Add(diagnostics.NewError(msg).
		WithCode(diagnostics.ErrUninitializedVariable).
		WithPrimaryLabel(use.Location, msg).
		WithHelp("assign a value before reading this symbol"))
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
