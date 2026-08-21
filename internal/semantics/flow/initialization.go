package flow

import (
	"fmt"

	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/symbols"
)

type State map[symbols.SymbolID]struct{}

type FunctionResult struct {
	In  map[cfg.SiteID]State
	Out map[cfg.SiteID]State
}

type Result map[ir.NodeID]*FunctionResult

type site struct {
	flow *cfg.Site
	stmt hir.Stmt
	term cfg.Terminator
}

// Analyze computes definite initialization over finalized CFG sites.
func Analyze(module *hir.Module, graphs []*cfg.Graph, diag *diagnostics.DiagnosticBag) Result {
	result := make(Result)
	if module == nil {
		return result
	}
	for index, fn := range module.Funcs {
		if fn == nil || index >= len(graphs) || graphs[index] == nil {
			continue
		}
		result[fn.NodeID] = analyzeFunction(fn, graphs[index], diag)
	}
	return result
}

func analyzeFunction(fn *hir.Function, graph *cfg.Graph, diag *diagnostics.DiagnosticBag) *FunctionResult {
	nodes, order, tracked := indexSites(fn, graph)
	result := &FunctionResult{In: make(map[cfg.SiteID]State), Out: make(map[cfg.SiteID]State)}
	if graph == nil || graph.Entry == nil || len(graph.Entry.Sites) == 0 {
		return result
	}
	entry := graph.Entry.Sites[0].ID
	entryState := make(State)
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
		for _, edge := range node.flow.Successors {
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
			node := &site{flow: flowSite}
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

func transfer(node *site, in State) State {
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

func checkReads(node *site, state State, tracked map[symbols.SymbolID]string, diag *diagnostics.DiagnosticBag) {
	if node == nil || diag == nil {
		return
	}
	checkExpr := func(expr ir.Expr) {
		walkExpr(expr, func(ident *ir.Ident) {
			name, local := tracked[ident.SymbolID]
			if !local {
				return
			}
			if _, initialized := state[ident.SymbolID]; initialized {
				return
			}
			if name == "" {
				name = ident.Name
			}
			msg := "symbol `" + name + "` used before it's initialized"
			diag.Add(diagnostics.NewError(msg).
				WithCode(diagnostics.ErrUninitializedVariable).
				WithPrimaryLabel(ident.Location, msg).
				WithHelp("assign a value before reading this symbol"))
		})
	}
	switch stmt := node.stmt.(type) {
	case *hir.Binding:
		checkExpr(stmt.Value)
	case *hir.Assign:
		checkExpr(stmt.Value)
		if _, direct := directAssignedIdent(stmt.Target); !direct {
			walkPlace(stmt.Target, checkExpr)
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

func walkPlace(place *ir.Place, visit func(ir.Expr)) {
	if place == nil {
		return
	}
	visit(place.Root)
	for _, projection := range place.Projections {
		visit(projection.Index)
	}
}

func walkExpr(expr ir.Expr, visit func(*ir.Ident)) {
	if expr == nil {
		return
	}
	switch node := expr.(type) {
	case *ir.InvalidExpr, *ir.IntLit, *ir.FloatLit, *ir.StringLit, *ir.BoolLit, *ir.ZeroValue:
		return
	case *ir.Ident:
		visit(node)
	case *ir.OptionalSome:
		walkExpr(node.Value, visit)
	case *ir.Unary:
		walkExpr(node.Arg, visit)
	case *ir.Binary:
		walkExpr(node.Left, visit)
		walkExpr(node.Right, visit)
	case *ir.Call:
		walkExpr(node.Callee, visit)
		for _, arg := range node.Args {
			walkExpr(arg, visit)
		}
	case *ir.Load:
		walkPlace(node.Place, func(value ir.Expr) { walkExpr(value, visit) })
	case *ir.AddrOf:
		walkPlace(node.Place, func(value ir.Expr) { walkExpr(value, visit) })
	case *ir.TempBorrow:
		walkExpr(node.Value, visit)
	case *ir.Len:
		walkExpr(node.Value, visit)
	case *ir.StringChars:
		walkExpr(node.Value, visit)
	case *ir.SliceView:
		walkPlace(node.Source, func(value ir.Expr) { walkExpr(value, visit) })
		walkExpr(node.Start, visit)
		walkExpr(node.End, visit)
	case *ir.InterfaceMake:
		walkExpr(node.Value, visit)
	case *ir.InterfaceCall:
		walkExpr(node.Base, visit)
		for _, arg := range node.Args {
			walkExpr(arg, visit)
		}
	case *ir.Field:
		walkExpr(node.Base, visit)
	case *ir.StructLit:
		for _, field := range node.Fields {
			walkExpr(field, visit)
		}
	case *ir.ArrayLit:
		for _, value := range node.Values {
			walkExpr(value, visit)
		}
	case *ir.DynamicArrayOp:
		walkExpr(node.Array, visit)
		walkExpr(node.Length, visit)
		walkExpr(node.Value, visit)
	case *ir.AllocExpr:
		walkExpr(node.Value, visit)
		walkExpr(node.Allocator, visit)
	case *ir.Cast:
		walkExpr(node.Expr, visit)
	case *ir.Print:
		walkExpr(node.Value, visit)
	case *ir.Drop:
		walkExpr(node.Value, visit)
	default:
		panic(fmt.Sprintf("unhandled HIR expression %T in definite initialization", expr))
	}
}

func copyState(state State) State {
	copy := make(State, len(state))
	for symbol := range state {
		copy[symbol] = struct{}{}
	}
	return copy
}

func intersectState(left, right State) State {
	intersection := make(State)
	for symbol := range left {
		if _, present := right[symbol]; present {
			intersection[symbol] = struct{}{}
		}
	}
	return intersection
}

func equalState(left, right State) bool {
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
