package definiteinit

import (
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
)

type state map[symbols.SymbolID]struct{}

type functionResult struct {
	In  map[cfg.SiteID]state
	Out map[cfg.SiteID]state
}

type site struct {
	cfgSite   *cfg.Site
	stmt      ast.Stmt
	condition ast.Expr
	scope     *table.Scope
}

// Check diagnoses reads not initialized on every reachable CFG predecessor.
func Check(
	graphs *cfg.Module,
	nodes map[ast.NodeID]ast.Node,
	blockScopes map[ast.NodeID]*table.Scope,
	resolvedSymbols map[ast.NodeID]*symbols.Symbol,
	diag *diagnostics.DiagnosticBag,
) {
	if graphs == nil {
		return
	}
	for _, graph := range graphs.Functions {
		if graph == nil {
			continue
		}
		fn, _ := nodes[ast.NodeID(graph.NodeID)].(*ast.FnDecl)
		if fn == nil {
			continue
		}
		analyzeFunction(fn, graph, nodes, blockScopes, resolvedSymbols, diag)
	}
}

func analyzeFunction(
	fn *ast.FnDecl,
	graph *cfg.Graph,
	nodes map[ast.NodeID]ast.Node,
	blockScopes map[ast.NodeID]*table.Scope,
	resolvedSymbols map[ast.NodeID]*symbols.Symbol,
	diag *diagnostics.DiagnosticBag,
) *functionResult {
	sites, order, tracked := indexSites(fn, graph, nodes, blockScopes)
	result := &functionResult{In: make(map[cfg.SiteID]state), Out: make(map[cfg.SiteID]state)}
	if graph == nil || graph.Entry == nil || len(graph.Entry.Sites) == 0 {
		return result
	}
	entry := graph.Entry.Sites[0].ID
	entryState := make(state)
	if fn != nil && fn.Body != nil {
		functionScope := blockScopes[fn.Body.ID()]
		for _, param := range fn.ParamsWithReceiver() {
			if param.Name == nil {
				continue
			}
			symbol, found := functionScope.Lookup(param.Name.Name)
			if !found || symbol == nil {
				continue
			}
			entryState[symbol.ID] = struct{}{}
			tracked[symbol.ID] = symbol.Name
		}
	}
	result.In[entry] = entryState
	queue := []cfg.SiteID{entry}
	queued := map[cfg.SiteID]bool{entry: true}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		queued[id] = false
		node := sites[id]
		if node == nil {
			continue
		}
		out := transfer(node, result.In[id])
		result.Out[id] = out
		for _, edge := range node.cfgSite.Successors {
			if sites[edge.To] == nil {
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
		if initialized, reachable := result.In[id]; reachable {
			checkReads(sites[id], initialized, tracked, resolvedSymbols, diag)
		}
	}
	return result
}

func indexSites(
	fn *ast.FnDecl,
	graph *cfg.Graph,
	nodes map[ast.NodeID]ast.Node,
	blockScopes map[ast.NodeID]*table.Scope,
) (map[cfg.SiteID]*site, []cfg.SiteID, map[symbols.SymbolID]string) {
	sites := make(map[cfg.SiteID]*site)
	order := make([]cfg.SiteID, 0)
	tracked := make(map[symbols.SymbolID]string)
	if fn == nil || graph == nil {
		return sites, order, tracked
	}
	for _, block := range graph.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		for _, cfgSite := range block.Sites {
			if cfgSite == nil {
				continue
			}
			indexed := &site{
				cfgSite: cfgSite,
				scope:   blockScopes[ast.NodeID(cfgSite.ScopeID)],
			}
			if stmt, ok := nodes[ast.NodeID(cfgSite.NodeID)].(ast.Stmt); ok {
				indexed.stmt = stmt
			}
			if branch, ok := block.Terminator.(*cfg.Branch); ok && cfgSite.Kind == cfg.SiteTerminator {
				indexed.condition, _ = nodes[ast.NodeID(branch.ConditionID)].(ast.Expr)
			}
			switch binding := indexed.stmt.(type) {
			case *ast.LetDecl:
				if indexed.scope != nil {
					if symbol, found := indexed.scope.LookupNode(binding); found && symbol != nil {
						tracked[symbol.ID] = symbol.Name
					}
				}
			case *ast.ConstDecl:
				if indexed.scope != nil {
					if symbol, found := indexed.scope.LookupNode(binding); found && symbol != nil {
						tracked[symbol.ID] = symbol.Name
					}
				}
			}
			sites[cfgSite.ID] = indexed
			order = append(order, cfgSite.ID)
		}
	}
	return sites, order, tracked
}

func transfer(node *site, in state) state {
	out := copyState(in)
	if node == nil || node.scope == nil {
		return out
	}
	switch stmt := node.stmt.(type) {
	case *ast.LetDecl:
		if stmt.Value != nil {
			if symbol, found := node.scope.LookupNode(stmt); found && symbol != nil {
				out[symbol.ID] = struct{}{}
			}
		}
	case *ast.ConstDecl:
		if stmt.Value != nil {
			if symbol, found := node.scope.LookupNode(stmt); found && symbol != nil {
				out[symbol.ID] = struct{}{}
			}
		}
	case *ast.AssignStmt:
		if ident, direct := stmt.Target.(*ast.Ident); direct && ident != nil {
			if symbol, found := node.scope.Lookup(ident.Name); found && symbol != nil {
				out[symbol.ID] = struct{}{}
			}
		}
	}
	return out
}

func checkReads(
	node *site,
	initialized state,
	tracked map[symbols.SymbolID]string,
	resolvedSymbols map[ast.NodeID]*symbols.Symbol,
	diag *diagnostics.DiagnosticBag,
) {
	if node == nil || diag == nil {
		return
	}
	checkExpr := func(expr ast.Expr) {
		ast.Inspect(expr, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			symbol := resolvedSymbols[ident.ID()]
			if symbol == nil {
				return true
			}
			name, local := tracked[symbol.ID]
			if !local {
				return true
			}
			if _, present := initialized[symbol.ID]; present {
				return true
			}
			if name == "" {
				name = ident.Name
			}
			name = ir.StripSymbolInstance(name)
			msg := "symbol `" + name + "` used before it's initialized"
			diag.Add(diagnostics.NewError(msg).
				WithCode(diagnostics.ErrUninitializedVariable).
				WithPrimaryLabel(ast.LocOf(ident), msg).
				WithHelp("assign a value before reading this symbol"))
			return true
		})
	}
	switch stmt := node.stmt.(type) {
	case *ast.LetDecl:
		checkExpr(stmt.Value)
	case *ast.ConstDecl:
		checkExpr(stmt.Value)
	case *ast.AssignStmt:
		checkExpr(stmt.Value)
		if _, direct := stmt.Target.(*ast.Ident); !direct {
			checkExpr(stmt.Target)
		}
	case *ast.ExprStmt:
		checkExpr(stmt.Expr)
	case *ast.ReturnStmt:
		checkExpr(stmt.Value)
	}
	checkExpr(node.condition)
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
