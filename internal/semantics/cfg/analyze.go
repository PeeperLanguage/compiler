package cfg

import (
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/problems"
	"compiler/internal/source"
)

// BuildModule lowers a HIR module into its canonical CFG artifact.
func BuildModule(hirMod *hir.Module) []*Graph {
	if hirMod == nil {
		return nil
	}
	graphs := make([]*Graph, 0, len(hirMod.Funcs))
	for _, fn := range hirMod.Funcs {
		graph := buildCFGFunction(hirMod.Types, fn)
		prepareGraph(graph)
		graphs = append(graphs, graph)
	}
	return graphs
}

// Analyze emits flow diagnostics from an already-built CFG artifact.
func Analyze(graphs []*Graph, diag *diagnostics.DiagnosticBag) {
	for _, fn := range graphs {
		if fn != nil {
			analyzeFunction(fn, diag)
		}
	}
}

func analyzeFunction(fn *Graph, diag *diagnostics.DiagnosticBag) {
	if fn == nil || fn.Entry == nil {
		return
	}
	prepareGraph(fn)

	for _, block := range fn.Blocks {
		if block == nil || block.Reachable || len(block.Stmts) == 0 {
			continue
		}
		loc := unreachableBlockLoc(block)
		diag.Add(problems.UnreachableCode(loc))
	}

	returnType, hasReturnType := fn.Types.Type(fn.ReturnType)
	if fn.Exit != nil && fn.Exit.Reachable && hasReturnType && returnType.Kind != ir.TypeVoid {
		reportMissingReturnCFG(fn, diag)
	}
}

// prepareGraph establishes structural facts every CFG consumer relies on.
// Analysis may call it again after a graph mutation before issuing diagnostics.
func prepareGraph(fn *Graph) {
	if fn == nil || fn.Entry == nil {
		return
	}
	for _, block := range fn.Blocks {
		if block != nil {
			block.Reachable = false
		}
	}
	markReachable(fn.Entry, make(map[int]bool))
	rebuildPredecessors(fn)
	rebuildSites(fn)
}

func rebuildSites(fn *Graph) {
	if fn == nil {
		return
	}
	for _, block := range fn.Blocks {
		if block == nil {
			continue
		}
		block.Sites = block.Sites[:0]
		for _, stmt := range block.Stmts {
			if stmt != nil {
				block.Sites = append(block.Sites, &Site{Kind: SiteStatement, NodeID: hir.NodeIDOf(stmt)})
			}
		}
		for _, scopeID := range block.ScopeExits {
			block.Sites = append(block.Sites, &Site{Kind: SiteScopeExit, NodeID: scopeID})
		}
		switch term := block.Terminator.(type) {
		case *Branch:
			block.Sites = append(block.Sites, &Site{Kind: SiteTerminator, NodeID: term.NodeID})
		}
		if len(block.Sites) == 0 {
			block.Sites = append(block.Sites, &Site{Kind: SiteJoin})
		}
		for index, site := range block.Sites {
			site.ID = SiteID{Block: block.ID, Index: index}
			site.Successors = site.Successors[:0]
			site.Predecessors = site.Predecessors[:0]
		}
	}
	for _, block := range fn.Blocks {
		if block == nil || len(block.Sites) == 0 {
			continue
		}
		for index := 0; index+1 < len(block.Sites); index++ {
			connectSites(block.Sites[index], block.Sites[index+1])
		}
		last := block.Sites[len(block.Sites)-1]
		switch term := block.Terminator.(type) {
		case *Jump:
			connectBlockSite(last, term.Target)
		case *Branch:
			connectBlockSite(last, term.TrueTarget)
			connectBlockSite(last, term.FalseTarget)
		case *Return:
			connectBlockSite(last, fn.Exit)
		}
	}
}

func connectBlockSite(from *Site, target *Block) {
	if target == nil || len(target.Sites) == 0 {
		return
	}
	connectSites(from, target.Sites[0])
}

func connectSites(from, to *Site) {
	if from == nil || to == nil {
		return
	}
	for _, existing := range from.Successors {
		if existing == to.ID {
			return
		}
	}
	from.Successors = append(from.Successors, to.ID)
	to.Predecessors = append(to.Predecessors, from.ID)
}

func markReachable(block *Block, seen map[int]bool) {
	if block == nil || seen[block.ID] {
		return
	}
	seen[block.ID] = true
	block.Reachable = true
	if block.Terminator == nil {
		return
	}
	for _, succ := range block.Terminator.Successors() {
		markReachable(succ, seen)
	}
}

func rebuildPredecessors(fn *Graph) {
	if fn == nil {
		return
	}
	for _, block := range fn.Blocks {
		if block != nil {
			block.Predecessors = block.Predecessors[:0]
		}
	}
	for _, block := range fn.Blocks {
		if block == nil || block.Terminator == nil {
			continue
		}
		for _, succ := range block.Terminator.Successors() {
			if succ != nil {
				succ.Predecessors = append(succ.Predecessors, block)
			}
		}
	}
}

func unreachableBlockLoc(block *Block) *source.Location {
	if block == nil || len(block.Stmts) == 0 {
		return block.Location
	}
	first := block.Stmts[0]
	if first == nil {
		return block.Location
	}
	loc := firstLoc(first)
	if loc != nil {
		return loc
	}
	return block.Location
}

func firstLoc(stmt hir.Stmt) *source.Location {
	switch s := stmt.(type) {
	case *hir.Binding:
		return s.Location
	case *hir.Return:
		return s.Location
	case *hir.ExprStmt:
		return s.Location
	case *hir.If:
		return s.Location
	case *hir.Block:
		return s.Location
	case *hir.Invalid:
		return s.Location
	default:
		return nil
	}
}

func reportMissingReturnCFG(fn *Graph, diag *diagnostics.DiagnosticBag) {
	if fn == nil || diag == nil || fn.Source == nil {
		return
	}
	msg := "not all control paths return a value"
	d := diagnostics.NewError(msg).WithCode(diagnostics.ErrMissingReturn)

	branches := filterMostSpecificBranches(findMissingReturnBranches(fn))
	if len(branches) > 0 && branches[0] != nil && branches[0].Location != nil {
		d.WithPrimaryLabel(branches[0].Location, "this branch does not return a value")
	} else if fn.Source.Location != nil {
		d.WithPrimaryLabel(fn.Source.Location, msg)
	}

	if fn.Source.Location != nil {
		d.WithSecondaryLabel(fn.Source.Location, "expected `"+fn.Types.Text(fn.ReturnType)+"` here")
	}

	for _, branch := range branches {
		if branch == nil || branch.Location == nil {
			continue
		}
		d.WithSecondaryLabel(branch.Location, "this branch does not return a value")
	}

	d.WithNote("some branch completes without a `return`, execution can fall off end of function")
	d.WithHelp("fulfill the return or add a fallback return on parent scope")
	diag.Add(d)
}

func filterMostSpecificBranches(blocks []*Block) []*Block {
	if len(blocks) <= 1 {
		return blocks
	}
	out := make([]*Block, 0, len(blocks))
	for i, b := range blocks {
		if b == nil || b.Location == nil {
			continue
		}
		isOuter := false
		for j, other := range blocks {
			if i == j || other == nil || other.Location == nil {
				continue
			}
			if locContains(b.Location, other.Location) && !locContains(other.Location, b.Location) {
				isOuter = true
				break
			}
		}
		if !isOuter {
			out = append(out, b)
		}
	}
	sortMissingBranches(out)
	return out
}

func locContains(outer, inner *source.Location) bool {
	if outer == nil || inner == nil || outer.Start == nil || outer.End == nil || inner.Start == nil || inner.End == nil {
		return false
	}
	of, inf := "", ""
	if outer.Filename != nil {
		of = *outer.Filename
	}
	if inner.Filename != nil {
		inf = *inner.Filename
	}
	if of != inf {
		return false
	}
	return !posLess(inner.Start, outer.Start) && !posLess(outer.End, inner.End)
}

func posLess(a, b *source.Position) bool {
	if a == nil || b == nil {
		return false
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Column < b.Column
}

func findMissingReturnBranches(fn *Graph) []*Block {
	if fn == nil || fn.Entry == nil || fn.Exit == nil {
		return nil
	}
	visited := make(map[*Block]bool)
	reachesExit := make(map[*Block]bool)

	var walk func(*Block) bool
	walk = func(block *Block) bool {
		if block == nil {
			return false
		}
		if block == fn.Exit {
			return true
		}
		if block.Returns {
			return false
		}
		if done, ok := visited[block]; ok {
			return done
		}
		visited[block] = false
		hitsExit := false
		if block.Terminator != nil {
			for _, succ := range block.Terminator.Successors() {
				if walk(succ) {
					hitsExit = true
				}
			}
		}
		visited[block] = hitsExit
		if hitsExit {
			reachesExit[block] = true
		}
		return hitsExit
	}
	_ = walk(fn.Entry)

	found := make([]*Block, 0)
	seen := make(map[*Block]bool)
	for block := range reachesExit {
		if block.BranchKind != "" {
			if !seen[block] {
				found = append(found, block)
				seen[block] = true
			}
			continue
		}
		queue := append([]*Block(nil), block.Predecessors...)
		traceSeen := make(map[*Block]bool)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if cur == nil || traceSeen[cur] {
				continue
			}
			traceSeen[cur] = true
			if cur.BranchKind != "" {
				if !seen[cur] {
					found = append(found, cur)
					seen[cur] = true
				}
				continue
			}
			queue = append(queue, cur.Predecessors...)
		}
	}

	sortMissingBranches(found)
	return found
}

func sortMissingBranches(blocks []*Block) {
	// Prefer deeper/inner by later source position.
	// Stable fallback to keep deterministic.
	for i := range blocks {
		for j := i + 1; j < len(blocks); j++ {
			if blocks[i] == nil || blocks[j] == nil {
				continue
			}
			if laterLoc(blocks[j].Location, blocks[i].Location) {
				blocks[i], blocks[j] = blocks[j], blocks[i]
			}
		}
	}
}

func laterLoc(a, b *source.Location) bool {
	if a == nil || a.Start == nil {
		return false
	}
	if b == nil || b.Start == nil {
		return true
	}
	if a.Start.Line != b.Start.Line {
		return a.Start.Line > b.Start.Line
	}
	return a.Start.Column > b.Start.Column
}
