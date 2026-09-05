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
	visitor := &validationVisitor{fn: fn, site: site, nodes: nodes}
	for index, op := range ops {
		visitor.index = index
		Visit(op, visitor)
	}
	for _, unclosed := range visitor.open {
		visitor.problems = append(visitor.problems, fmt.Sprintf("function %d site %v leaves call %d open", fn, site, unclosed))
	}
	return visitor.problems
}

type validationVisitor struct {
	fn       ir.NodeID
	site     cfg.SiteID
	index    int
	nodes    map[ast.NodeID]ast.Node
	problems []string
	open     []ast.NodeID
}

func (v *validationVisitor) where() string {
	return fmt.Sprintf("function %d site %v operation %d", v.fn, v.site, v.index)
}

func (v *validationVisitor) VisitDefine(op Define) {
	where := v.where()
	v.problems = append(v.problems, validateNode(where, "define", op.Symbol == nil, op.Node, v.nodes)...)
	if op.Value != 0 {
		v.problems = append(v.problems, validateNode(where, "define value", false, op.Value, v.nodes)...)
	}
}

func (v *validationVisitor) VisitWrite(op Write) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "write", op.Place)...)
	v.problems = append(v.problems, validateNode(where, "write", false, op.Node, v.nodes)...)
	v.problems = append(v.problems, validateNode(where, "write owner", false, op.Owner, v.nodes)...)
	if op.Value != 0 {
		v.problems = append(v.problems, validateNode(where, "write value", false, op.Value, v.nodes)...)
	}
}

func (v *validationVisitor) VisitUse(op Use) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "use", op.Place)...)
	v.problems = append(v.problems, validateNode(where, "use", false, op.Node, v.nodes)...)
	if op.Location == nil {
		v.problems = append(v.problems, where+" is a use with no source location to report against")
	}
}

func (v *validationVisitor) VisitBorrow(op Borrow) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "borrow", op.Place)...)
	v.problems = append(v.problems, validateNode(where, "borrow", false, op.Node, v.nodes)...)
	v.problems = append(v.problems, validateNode(where, "borrow operand", false, op.Operand, v.nodes)...)
	if op.Location == nil {
		v.problems = append(v.problems, where+" is a borrow with no source location to report against")
	}
}

func (v *validationVisitor) VisitIterate(op Iterate) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "iteration", op.Place)...)
	v.problems = append(v.problems, validateNode(where, "iteration", op.Carrier == nil, op.Node, v.nodes)...)
	v.problems = append(v.problems, validateNode(where, "iteration owner", false, op.Loop, v.nodes)...)
	if op.Location == nil {
		v.problems = append(v.problems, where+" is an iteration with no source location to report against")
	}
}

func (v *validationVisitor) VisitDiscard(op Discard) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "discard", op.Place)...)
	v.problems = append(v.problems, validateNode(where, "discard", false, op.Node, v.nodes)...)
	if op.Location == nil {
		v.problems = append(v.problems, where+" is a discard with no source location to report against")
	}
}

func (v *validationVisitor) VisitCallBegin(op CallBegin) {
	where := v.where()
	v.problems = append(v.problems, validateNode(where, "call start", false, op.Node, v.nodes)...)
	v.open = append(v.open, op.Node)
}

func (v *validationVisitor) VisitCallEnd(op CallEnd) {
	where := v.where()
	if len(v.open) == 0 {
		v.problems = append(v.problems, fmt.Sprintf("%s ends a call that never started", where))
		return
	}
	if last := v.open[len(v.open)-1]; last != op.Node {
		v.problems = append(v.problems, fmt.Sprintf("%s ends call %d while call %d is still open", where, op.Node, last))
	}
	v.open = v.open[:len(v.open)-1]
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
