package cfg

import (
	"compiler/internal/diagnostics"
	"compiler/internal/ir"
	"compiler/internal/problems"
	"compiler/internal/source"
)

// Analyze emits control-flow diagnostics without mutating finalized topology.
func Analyze(module *Module, diag *diagnostics.DiagnosticBag, constantCondition func(conditionID, scopeID ir.NodeID) (bool, bool)) {
	if module == nil {
		return
	}
	for _, fn := range module.Functions {
		analyzeFunction(fn, diag, constantCondition)
	}
}

func analyzeFunction(fn *Graph, diag *diagnostics.DiagnosticBag, constantCondition func(conditionID, scopeID ir.NodeID) (bool, bool)) {
	if fn == nil || fn.Entry == nil {
		return
	}
	if diag != nil {
		for _, block := range fn.Blocks {
			if block == nil {
				continue
			}
			if branch, ok := block.Terminator.(*Branch); ok && block.Origin != BlockLoop && constantCondition != nil {
				if value, found := constantCondition(branch.ConditionID, branch.ScopeID); found {
					reportConstantCondition(branch, value, diag)
				}
			}
			if block.Reachable {
				continue
			}
			for _, site := range block.Sites {
				if site != nil && (site.Kind == SiteStatement || site.Kind == SiteTerminator) {
					diag.Add(problems.UnreachableCode(site.Location))
				}
			}
		}
	}
	if fn.Exit != nil && fn.Exit.Reachable && fn.ReturnsValue {
		reportMissingReturn(fn, diag)
	}
}

func reportConstantCondition(branch *Branch, value bool, diag *diagnostics.DiagnosticBag) {
	if branch == nil || diag == nil {
		return
	}
	msg := "condition is always false"
	code := diagnostics.WarnConstantConditionFalse
	if value {
		msg = "condition is always true"
		code = diagnostics.WarnConstantConditionTrue
	}
	diag.Add(diagnostics.NewWarning(msg).WithCode(code).WithPrimaryLabel(branch.Location, msg))
}

func reportMissingReturn(fn *Graph, diag *diagnostics.DiagnosticBag) {
	if fn == nil || diag == nil {
		return
	}
	msg := "not all control paths return a value"
	diagnostic := diagnostics.NewError(msg).WithCode(diagnostics.ErrMissingReturn)
	branches := filterMostSpecificBranches(findMissingReturnBranches(fn))
	if len(branches) > 0 && branches[0] != nil && branches[0].Location != nil {
		diagnostic.WithPrimaryLabel(branches[0].Location, "this branch does not return a value")
	} else if fn.Location != nil {
		diagnostic.WithPrimaryLabel(fn.Location, msg)
	}
	if fn.Location != nil {
		diagnostic.WithSecondaryLabel(fn.Location, "expected `"+fn.ReturnTypeText+"` here")
	}
	for _, branch := range branches {
		if branch != nil && branch.Location != nil {
			diagnostic.WithSecondaryLabel(branch.Location, "this branch does not return a value")
		}
	}
	diagnostic.WithNote("some branch completes without a `return`, execution can fall off end of function")
	diagnostic.WithHelp("fulfill the return or add a fallback return on parent scope")
	diag.Add(diagnostic)
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
		if _, returns := block.Terminator.(*Return); returns {
			return false
		}
		if done, ok := visited[block]; ok {
			return done
		}
		visited[block] = false
		hitsExit := false
		if block.Terminator != nil {
			for _, successor := range block.Terminator.Successors() {
				if walk(successor) {
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
		if block.Origin != BlockNormal {
			if !seen[block] {
				found = append(found, block)
				seen[block] = true
			}
			continue
		}
		queue := append([]*Block(nil), block.Predecessors...)
		traceSeen := make(map[*Block]bool)
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if current == nil || traceSeen[current] {
				continue
			}
			traceSeen[current] = true
			if current.Origin != BlockNormal {
				if !seen[current] {
					found = append(found, current)
					seen[current] = true
				}
				continue
			}
			queue = append(queue, current.Predecessors...)
		}
	}
	sortMissingBranches(found)
	return found
}

func filterMostSpecificBranches(blocks []*Block) []*Block {
	if len(blocks) <= 1 {
		return blocks
	}
	out := make([]*Block, 0, len(blocks))
	for index, block := range blocks {
		if block == nil || block.Location == nil {
			continue
		}
		outer := false
		for otherIndex, other := range blocks {
			if index == otherIndex || other == nil || other.Location == nil {
				continue
			}
			if locContains(block.Location, other.Location) && !locContains(other.Location, block.Location) {
				outer = true
				break
			}
		}
		if !outer {
			out = append(out, block)
		}
	}
	sortMissingBranches(out)
	return out
}

func locContains(outer, inner *source.Location) bool {
	if outer == nil || inner == nil || outer.Start == nil || outer.End == nil || inner.Start == nil || inner.End == nil {
		return false
	}
	outerFile, innerFile := "", ""
	if outer.Filename != nil {
		outerFile = *outer.Filename
	}
	if inner.Filename != nil {
		innerFile = *inner.Filename
	}
	return outerFile == innerFile && !posLess(inner.Start, outer.Start) && !posLess(outer.End, inner.End)
}

func posLess(left, right *source.Position) bool {
	if left == nil || right == nil {
		return false
	}
	if left.Line != right.Line {
		return left.Line < right.Line
	}
	return left.Column < right.Column
}

func sortMissingBranches(blocks []*Block) {
	for index := range blocks {
		for other := index + 1; other < len(blocks); other++ {
			if blocks[index] != nil && blocks[other] != nil && laterLoc(blocks[other].Location, blocks[index].Location) {
				blocks[index], blocks[other] = blocks[other], blocks[index]
			}
		}
	}
}

func laterLoc(left, right *source.Location) bool {
	if left == nil || left.Start == nil {
		return false
	}
	if right == nil || right.Start == nil {
		return true
	}
	if left.Start.Line != right.Start.Line {
		return left.Start.Line > right.Start.Line
	}
	return left.Start.Column > right.Start.Column
}
