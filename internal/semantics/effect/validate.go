package effect

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
)

const maxReportedProblems = 10

// Validate checks the shape of published effects: that every operation names a
// symbol, that a read can be reported against a source span, and that every key
// refers to a site that exists in the graph it claims.
//
// It deliberately does not re-derive meaning. Whether a read should have been
// published for some expression is the producer's decision, and re-deciding it
// here would be a second implementation of the thing being validated. A missing
// operation is caught by the dispatch contract in internal/contracts, not here.
func (r Result) Validate(graphs *cfg.Module, nodes map[ast.NodeID]ast.Node) error {
	if len(r) == 0 {
		return nil
	}
	problems := make([]string, 0)
	sitesByFunction := make(map[ir.NodeID]map[cfg.SiteID]struct{})
	if graphs != nil {
		for _, graph := range graphs.Functions {
			if graph == nil {
				continue
			}
			sitesByFunction[graph.NodeID] = graphSites(graph)
		}
	}
	for fn, siteOps := range r {
		known, found := sitesByFunction[fn]
		if !found {
			problems = append(problems, fmt.Sprintf("function %d has published effects but no control-flow graph", fn))
			continue
		}
		for site, ops := range siteOps {
			if _, exists := known[site]; !exists {
				problems = append(problems, fmt.Sprintf("function %d publishes effects at site %v, which the graph does not contain", fn, site))
				continue
			}
			problems = append(problems, validateOps(fn, site, ops, nodes)...)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	// Map iteration order is unspecified, so an unsorted report would differ
	// between runs of the same broken artifact.
	sort.Strings(problems)
	if len(problems) > maxReportedProblems {
		return fmt.Errorf("%s (%d more)", strings.Join(problems[:maxReportedProblems], "; "), len(problems)-maxReportedProblems)
	}
	return errors.New(strings.Join(problems, "; "))
}

func validateOps(fn ir.NodeID, site cfg.SiteID, ops []Op, nodes map[ast.NodeID]ast.Node) []string {
	problems := make([]string, 0)
	open := make([]ast.NodeID, 0)
	for index, op := range ops {
		where := fmt.Sprintf("function %d site %v operation %d", fn, site, index)
		switch op := op.(type) {
		case Define:
			problems = append(problems, validateNode(where, "define", op.Symbol == nil, op.Node, nodes)...)
		case Write:
			problems = append(problems, validateNode(where, "write", op.Symbol == nil, op.Node, nodes)...)
		case Use:
			problems = append(problems, validatePlace(where, "use", op.Place)...)
			problems = append(problems, validateNode(where, "use", false, op.Node, nodes)...)
			if op.Location == nil {
				problems = append(problems, where+" is a use with no source location to report against")
			}
		case Borrow:
			problems = append(problems, validateNode(where, "borrow", false, op.Node, nodes)...)
			if op.Location == nil {
				problems = append(problems, where+" is a borrow with no source location to report against")
			}
		case Discard:
			problems = append(problems, validatePlace(where, "discard", op.Place)...)
			problems = append(problems, validateNode(where, "discard", false, op.Node, nodes)...)
			if op.Location == nil {
				problems = append(problems, where+" is a discard with no source location to report against")
			}
		case CallBegin:
			problems = append(problems, validateNode(where, "call start", false, op.Node, nodes)...)
			open = append(open, op.Node)
		case CallEnd:
			// A consumer restores state saved at the matching start, so an
			// unbalanced or crossed pair would restore the wrong mark.
			if len(open) == 0 {
				problems = append(problems, fmt.Sprintf("%s ends a call that never started", where))
				continue
			}
			if last := open[len(open)-1]; last != op.Node {
				problems = append(problems, fmt.Sprintf("%s ends call %d while call %d is still open", where, op.Node, last))
			}
			open = open[:len(open)-1]
		default:
			problems = append(problems, fmt.Sprintf("%s has unknown effect kind %T", where, op))
		}
	}
	for _, unclosed := range open {
		problems = append(problems, fmt.Sprintf("function %d site %v leaves call %d open", fn, site, unclosed))
	}
	return problems
}

// validatePlace enforces that a place names exactly one root. A place with
// neither names nothing; one with both would let a consumer reach two different
// answers depending on which field it read.
func validatePlace(where, kind string, at Place) []string {
	switch {
	case at.Root == nil && at.Temporary == 0:
		return []string{fmt.Sprintf("%s is a %s whose place names neither a binding nor a temporary", where, kind)}
	case at.Root != nil && at.Temporary != 0:
		return []string{fmt.Sprintf("%s is a %s whose place names both binding %s and temporary %d",
			where, kind, at.Root.Name, at.Temporary)}
	}
	return nil
}

func validateNode(where, kind string, missingSymbol bool, node ast.NodeID, nodes map[ast.NodeID]ast.Node) []string {
	problems := make([]string, 0, 2)
	if missingSymbol {
		problems = append(problems, fmt.Sprintf("%s is a %s with no symbol", where, kind))
	}
	if nodes == nil {
		return problems
	}
	if _, exists := nodes[node]; !exists {
		problems = append(problems, fmt.Sprintf("%s is a %s naming node %d, which is not in the typed AST", where, kind, node))
	}
	return problems
}

func graphSites(graph *cfg.Graph) map[cfg.SiteID]struct{} {
	sites := make(map[cfg.SiteID]struct{})
	for _, block := range graph.Blocks {
		if block == nil {
			continue
		}
		for _, site := range block.Sites {
			if site != nil {
				sites[site.ID] = struct{}{}
			}
		}
	}
	return sites
}
