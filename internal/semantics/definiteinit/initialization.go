package definiteinit

import (
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/symbols"
)

type state map[symbols.SymbolID]struct{}

type functionResult struct {
	In  map[cfg.SiteID]state
	Out map[cfg.SiteID]state
}

type site struct {
	cfgSite *cfg.Site
	stmt    hir.Stmt
	term    cfg.Terminator
}

// Check diagnoses reads not initialized on every reachable CFG predecessor.
func Check(module *hir.Module, graphs []*cfg.Graph, diag *diagnostics.DiagnosticBag) {
	if module == nil {
		return
	}
	for index, fn := range module.Funcs {
		if fn == nil || index >= len(graphs) || graphs[index] == nil {
			continue
		}
		analyzeFunction(fn, graphs[index], diag)
	}
}

func analyzeFunction(fn *hir.Function, graph *cfg.Graph, diag *diagnostics.DiagnosticBag) *functionResult {
	nodes, order, tracked := indexSites(fn, graph)
	result := &functionResult{In: make(map[cfg.SiteID]state), Out: make(map[cfg.SiteID]state)}
	if graph == nil || graph.Entry == nil || len(graph.Entry.Sites) == 0 {
		return result
	}
	entry := graph.Entry.Sites[0].ID
	entryState := make(state)
	for _, param := range fn.Params {
		if param.SymbolID != 0 {
			entryState[param.SymbolID] = struct{}{}
		}
	}
	result.In[entry] = entryState
	queue := []cfg.SiteID{entry}
	queued := map[cfg.SiteID]bool{entry: true}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		queued[id] = false
		node := nodes[id]
		if node == nil {
			continue
		}
		out := transfer(node, result.In[id])
		result.Out[id] = out
		for _, edge := range node.cfgSite.Successors {
			if nodes[edge.To] == nil {
				continue
			}
			current, exists := result.In[edge.To]
			merged := copyState(out)
			if exists {
				merged = intersectState(current, out)
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
	for _, id := range order {
		if state, reachable := result.In[id]; reachable {
			checkReads(nodes[id], state, tracked, diag)
		}
	}
	return result
}

func indexSites(fn *hir.Function, graph *cfg.Graph) (map[cfg.SiteID]*site, []cfg.SiteID, map[symbols.SymbolID]string) {
	nodes := make(map[cfg.SiteID]*site)
	order := make([]cfg.SiteID, 0)
	tracked := make(map[symbols.SymbolID]string)
	for _, param := range fn.Params {
		if param.SymbolID != 0 {
			tracked[param.SymbolID] = param.Name
		}
	}
	for _, block := range graph.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		statementIndex := 0
		for _, flowSite := range block.Sites {
			if flowSite == nil {
				continue
			}
			node := &site{cfgSite: flowSite}
			switch flowSite.Kind {
			case cfg.SiteStatement:
				if statementIndex < len(block.Stmts) {
					node.stmt = block.Stmts[statementIndex]
					if binding, ok := node.stmt.(*hir.Binding); ok && binding.SymbolID != 0 {
						tracked[binding.SymbolID] = binding.Name
					}
				}
				statementIndex++
			case cfg.SiteTerminator:
				node.term = block.Terminator
			}
			nodes[flowSite.ID] = node
			order = append(order, flowSite.ID)
		}
	}
	return nodes, order, tracked
}

func transfer(node *site, in state) state {
	out := copyState(in)
	if node == nil {
		return out
	}
	switch stmt := node.stmt.(type) {
	case *hir.Binding:
		if stmt.Value != nil && stmt.SymbolID != 0 {
			out[stmt.SymbolID] = struct{}{}
		}
	case *hir.Assign:
		if ident, direct := directAssignedIdent(stmt.Target); direct && ident.SymbolID != 0 {
			out[ident.SymbolID] = struct{}{}
		}
	}
	return out
}

func checkReads(node *site, initialized state, tracked map[symbols.SymbolID]string, diag *diagnostics.DiagnosticBag) {
	if node == nil || diag == nil {
		return
	}
	checkRead := func(expr ir.Expr) bool {
		ident, ok := expr.(*ir.Ident)
		if !ok {
			return true
		}
		name, local := tracked[ident.SymbolID]
		if !local {
			return true
		}
		if _, present := initialized[ident.SymbolID]; present {
			return true
		}
		if name == "" {
			name = ident.Name
		}
		name = ir.StripSymbolInstance(name)
		msg := "symbol `" + name + "` used before it's initialized"
		diag.Add(diagnostics.NewError(msg).
			WithCode(diagnostics.ErrUninitializedVariable).
			WithPrimaryLabel(ident.Location, msg).
			WithHelp("assign a value before reading this symbol"))
		return true
	}
	checkExpr := func(expr ir.Expr) { ir.InspectExpr(expr, checkRead) }
	switch stmt := node.stmt.(type) {
	case *hir.Binding:
		checkExpr(stmt.Value)
	case *hir.Assign:
		checkExpr(stmt.Value)
		if _, direct := directAssignedIdent(stmt.Target); !direct {
			ir.InspectPlace(stmt.Target, checkRead)
		}
	case *hir.ExprStmt:
		checkExpr(stmt.Value)
	case *hir.Return:
		checkExpr(stmt.Value)
		for _, cleanup := range stmt.Cleanup {
			checkExpr(cleanup)
		}
	}
	if branch, ok := node.term.(*cfg.Branch); ok {
		checkExpr(branch.Cond)
	}
}

func directAssignedIdent(place *ir.Place) (*ir.Ident, bool) {
	if place == nil || len(place.Projections) != 0 {
		return nil, false
	}
	ident, ok := place.Root.(*ir.Ident)
	return ident, ok && ident != nil
}

func copyState(current state) state {
	copy := make(state, len(current))
	for symbol := range current {
		copy[symbol] = struct{}{}
	}
	return copy
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
