package cfg

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// maxReportedProblems bounds one internal error so a systematic construction
// break reports a readable sample instead of one line per block in the module.
const maxReportedProblems = 10

// Validate checks that finalized control-flow topology is internally
// consistent: blocks and sites are identified as construction promises, every
// reachable block terminates, edges agree with the terminator that produced
// them, and every adjacency is recorded from both ends. A failure means CFG
// construction produced a malformed graph, so callers report it as an internal
// error, never as a source diagnostic.
//
// This is deliberately disjoint from Analyze. Analyze reports problems in the
// user's program — unreachable code, constant conditions, a missing return —
// against a graph it assumes well-formed. Validate checks the graph itself and
// says nothing about the program. Neither re-derives a semantic decision.
func (m *Module) Validate() error {
	if m == nil {
		return nil
	}
	problems := make([]string, 0)
	for _, fn := range m.Functions {
		if fn == nil {
			problems = append(problems, "module holds a nil function graph")
			continue
		}
		problems = append(problems, validateGraph(fn)...)
	}
	if len(problems) == 0 {
		return nil
	}
	// Reported in a stable order: callers compare internal errors across runs,
	// and block iteration is the only ordered part of the walk.
	sort.Strings(problems)
	total := len(problems)
	if total > maxReportedProblems {
		problems = problems[:maxReportedProblems]
		return fmt.Errorf("%s (%d more)", strings.Join(problems, "; "), total-maxReportedProblems)
	}
	return errors.New(strings.Join(problems, "; "))
}

func validateGraph(fn *Graph) []string {
	problems := validateBlockIdentity(fn)
	// Every later check indexes blocks by ID, so a broken index makes their
	// output noise rather than evidence.
	if len(problems) > 0 {
		return problems
	}
	problems = append(problems, validateTermination(fn)...)
	problems = append(problems, validateBlockAdjacency(fn)...)
	problems = append(problems, validateSites(fn)...)
	problems = append(problems, validateReachability(fn)...)
	return problems
}

// validateBlockIdentity checks the promise every other check depends on: block
// IDs are dense indexes into Blocks, and entry and exit are blocks of this
// graph rather than of another one.
func validateBlockIdentity(fn *Graph) []string {
	problems := make([]string, 0)
	for index, block := range fn.Blocks {
		if block == nil {
			problems = append(problems, fmt.Sprintf("function %d holds a nil block at index %d", fn.NodeID, index))
			continue
		}
		if block.ID != index {
			problems = append(problems, fmt.Sprintf("function %d block at index %d identifies as b%d", fn.NodeID, index, block.ID))
		}
	}
	if len(problems) > 0 {
		return problems
	}
	if fn.Entry == nil {
		problems = append(problems, fmt.Sprintf("function %d has no entry block", fn.NodeID))
	} else if !ownsBlock(fn, fn.Entry) {
		problems = append(problems, fmt.Sprintf("function %d entry b%d is not one of its blocks", fn.NodeID, fn.Entry.ID))
	}
	if fn.Exit == nil {
		problems = append(problems, fmt.Sprintf("function %d has no exit block", fn.NodeID))
	} else if !ownsBlock(fn, fn.Exit) {
		problems = append(problems, fmt.Sprintf("function %d exit b%d is not one of its blocks", fn.NodeID, fn.Exit.ID))
	}
	return problems
}

// validateTermination checks that control leaves every reachable block. The
// exit block is the one exception: it is where control stops.
func validateTermination(fn *Graph) []string {
	problems := make([]string, 0)
	for _, block := range fn.Blocks {
		if block == fn.Exit {
			if block.Terminator != nil {
				problems = append(problems, fmt.Sprintf("function %d exit b%d carries a terminator", fn.NodeID, block.ID))
			}
			continue
		}
		if block.Terminator == nil {
			// An unreachable block may legitimately have been abandoned mid
			// construction; a reachable one leaves control nowhere.
			if block.Reachable {
				problems = append(problems, fmt.Sprintf("function %d reachable block b%d has no terminator", fn.NodeID, block.ID))
			}
			continue
		}
		for _, successor := range block.Terminator.Successors() {
			if successor == nil {
				problems = append(problems, fmt.Sprintf("function %d block b%d transfers to a nil block", fn.NodeID, block.ID))
			} else if !ownsBlock(fn, successor) {
				problems = append(problems, fmt.Sprintf("function %d block b%d transfers to b%d, which is not one of its blocks", fn.NodeID, block.ID, successor.ID))
			}
		}
		if variant, ok := block.Terminator.(*SwitchVariant); ok {
			problems = append(problems, validateVariantCases(fn, block, variant)...)
		}
	}
	return problems
}

// validateVariantCases checks that a variant switch selects each case once.
// Which cases a switch should carry is a typechecking decision; that two
// targets claim the same one is a topology defect, because the second is
// unreachable through the edge that names it.
func validateVariantCases(fn *Graph, block *Block, term *SwitchVariant) []string {
	problems := make([]string, 0)
	seen := make(map[int]bool, len(term.Targets))
	for _, target := range term.Targets {
		if target.Case < 0 {
			problems = append(problems, fmt.Sprintf("function %d block b%d switches on negative case %d", fn.NodeID, block.ID, target.Case))
		}
		if seen[target.Case] {
			problems = append(problems, fmt.Sprintf("function %d block b%d switches twice on case %d", fn.NodeID, block.ID, target.Case))
		}
		seen[target.Case] = true
	}
	return problems
}

// validateBlockAdjacency checks that block-level predecessors record exactly
// the transfers terminators make. A consumer walking backwards must see the
// same graph as one walking forwards.
func validateBlockAdjacency(fn *Graph) []string {
	problems := make([]string, 0)
	if fn.BlockEdges == nil {
		return append(problems, fmt.Sprintf("function %d has no block topology", fn.NodeID))
	}
	forward := make(map[[2]int]bool)
	for _, block := range fn.Blocks {
		if block.Terminator == nil {
			continue
		}
		for _, successor := range block.Terminator.Successors() {
			if successor != nil && ownsBlock(fn, successor) {
				forward[[2]int{block.ID, successor.ID}] = true
			}
		}
	}
	for _, block := range fn.Blocks {
		for _, edge := range fn.BlockEdges.OutEdges(block.ID) {
			pair := [2]int{edge.From, edge.To}
			if edge.From != block.ID {
				problems = append(problems, fmt.Sprintf("function %d block b%d owns an edge leaving b%d", fn.NodeID, block.ID, edge.From))
			}
			if !forward[pair] {
				problems = append(problems, fmt.Sprintf("function %d block topology records b%d -> b%d, but the terminator does not", fn.NodeID, edge.From, edge.To))
			}
			delete(forward, pair)
		}
	}
	for pair := range forward {
		problems = append(problems, fmt.Sprintf("function %d block b%d transfers to b%d, which is absent from block topology", fn.NodeID, pair[0], pair[1]))
	}
	return problems
}

// validateSites checks the program points consumers key evidence against: every
// block owns at least one, each carries the identity its position implies, and
// every site edge resolves, agrees with the terminator that produced it, and is
// recorded from both ends.
func validateSites(fn *Graph) []string {
	problems := make([]string, 0)
	for _, block := range fn.Blocks {
		if len(block.Sites) == 0 {
			problems = append(problems, fmt.Sprintf("function %d block b%d owns no site", fn.NodeID, block.ID))
			continue
		}
		for index, site := range block.Sites {
			if site == nil {
				problems = append(problems, fmt.Sprintf("function %d block b%d holds a nil site at index %d", fn.NodeID, block.ID, index))
				continue
			}
			want := SiteID{Block: block.ID, Index: index}
			if site.ID != want {
				problems = append(problems, fmt.Sprintf("function %d site at b%d[%d] identifies as b%d[%d]", fn.NodeID, block.ID, index, site.ID.Block, site.ID.Index))
			}
			if site.Kind == SiteScopeExit && site.ScopeID == 0 {
				problems = append(problems, fmt.Sprintf("function %d scope exit at b%d[%d] names no scope", fn.NodeID, block.ID, index))
			}
		}
	}
	if len(problems) > 0 {
		return problems
	}
	return validateSiteEdges(fn)
}

func validateSiteEdges(fn *Graph) []string {
	problems := make([]string, 0)
	if fn.SiteEdges == nil {
		return append(problems, fmt.Sprintf("function %d has no site topology", fn.NodeID))
	}
	for _, block := range fn.Blocks {
		last := len(block.Sites) - 1
		for index, site := range block.Sites {
			for _, edge := range fn.SiteEdges.OutEdges(site.ID) {
				if edge.From != site.ID {
					problems = append(problems, fmt.Sprintf("function %d site b%d[%d] owns an edge leaving %v", fn.NodeID, block.ID, index, edge.From))
				}
				if siteAt(fn, edge.To) == nil {
					problems = append(problems, fmt.Sprintf("function %d site b%d[%d] transfers to %v, which is not a site", fn.NodeID, block.ID, index, edge.To))
					continue
				}
				if kind, ok := expectedEdgeKind(block, index == last, edge.Kind); !ok {
					problems = append(problems, fmt.Sprintf("function %d site b%d[%d] leaves on a %s edge, but %s", fn.NodeID, block.ID, index, edgeKindName(edge.Kind), kind))
				}
			}
			for _, edge := range fn.SiteEdges.InEdges(site.ID) {
				if edge.To != site.ID {
					problems = append(problems, fmt.Sprintf("function %d site b%d[%d] records an edge arriving at %v", fn.NodeID, block.ID, index, edge.To))
				}
				if siteAt(fn, edge.From) == nil {
					problems = append(problems, fmt.Sprintf("function %d site b%d[%d] arrives from %v, which is not a site", fn.NodeID, block.ID, index, edge.From))
				}
			}
		}
	}
	return problems
}

// expectedEdgeKind reports whether one outgoing edge kind is legal at a site.
// Edges between sites within a block are plain sequence; only the block's last
// site leaves on the terminator, and then the kind must name that terminator's
// meaning. It returns the expectation to quote when the kind is wrong.
func expectedEdgeKind(block *Block, last bool, kind EdgeKind) (string, bool) {
	if !last {
		if kind == EdgeNormal {
			return "", true
		}
		return "a site inside a block leaves only on a normal edge", false
	}
	switch block.Terminator.(type) {
	case *Jump:
		return "a jump leaves only on a normal edge", kind == EdgeNormal
	case *Branch:
		return "a branch leaves only on a true or false edge", kind == EdgeTrue || kind == EdgeFalse
	case *Return:
		return "a return leaves only on a return edge", kind == EdgeReturn
	case *SwitchVariant:
		return "a variant switch leaves only on a variant-case edge", kind == EdgeVariantCase
	case nil:
		return "a block with no terminator leaves on no edge", false
	}
	return "", true
}

// validateReachability checks the flag consumers trust against the traversal it
// claims to summarize. Analyze reports unreachable user code from this flag, so
// a stale flag turns a construction defect into a wrong diagnostic.
func validateReachability(fn *Graph) []string {
	seen := make(map[int]bool, len(fn.Blocks))
	var walk func(block *Block)
	walk = func(block *Block) {
		if block == nil || seen[block.ID] {
			return
		}
		seen[block.ID] = true
		if block.Terminator == nil {
			return
		}
		for _, successor := range block.Terminator.Successors() {
			walk(successor)
		}
	}
	walk(fn.Entry)

	problems := make([]string, 0)
	for _, block := range fn.Blocks {
		if block.Reachable != seen[block.ID] {
			problems = append(problems, fmt.Sprintf("function %d block b%d is marked reachable=%t but entry traversal says %t", fn.NodeID, block.ID, block.Reachable, seen[block.ID]))
		}
	}
	return problems
}

func ownsBlock(fn *Graph, block *Block) bool {
	return block.ID >= 0 && block.ID < len(fn.Blocks) && fn.Blocks[block.ID] == block
}

func siteAt(fn *Graph, id SiteID) *Site {
	if id.Block < 0 || id.Block >= len(fn.Blocks) {
		return nil
	}
	block := fn.Blocks[id.Block]
	if id.Index < 0 || id.Index >= len(block.Sites) {
		return nil
	}
	return block.Sites[id.Index]
}

func edgeKindName(kind EdgeKind) string {
	switch kind {
	case EdgeNormal:
		return "normal"
	case EdgeTrue:
		return "true"
	case EdgeFalse:
		return "false"
	case EdgeReturn:
		return "return"
	case EdgeVariantCase:
		return "variant-case"
	}
	return fmt.Sprintf("unknown(%d)", kind)
}
