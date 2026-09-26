package effect

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/pkg/typednil"
)

const maxReportedProblems = 10

// Validate checks operation identities, expression categories, storage roots,
// source locations, call brackets, and membership in the supplied CFG.
//
// It deliberately does not re-derive meaning. Whether a read should have been
// published for some expression is the producer's decision, and re-deciding it
// here would be a second implementation of the thing being validated. Dispatch
// contracts check node-kind coverage, not what each case publishes. Required
// operations and their order are covered by producer tests and source fixtures.
func (r Result) Validate(graphs *cfg.Module, source *thir.Module) error {
	if len(r) == 0 && graphs == nil {
		return nil
	}
	if source == nil {
		return errors.New("typed THIR is missing for effect validation")
	}
	problems := make([]string, 0)
	sitesByFunction := make(map[moduleid.FunctionID]map[cfg.SiteID]struct{})
	if graphs != nil {
		for _, graph := range graphs.Functions {
			if graph == nil {
				continue
			}
			sitesByFunction[graph.FunctionID] = graphSites(graph)
			if _, found := r[graph.FunctionID]; !found {
				problems = append(problems, fmt.Sprintf("function %s has a control-flow graph but no published effects", graph.FunctionID))
			}
		}
	}
	for fn, siteOps := range r {
		known, found := sitesByFunction[fn]
		if !found {
			problems = append(problems, fmt.Sprintf("function %s has published effects but no control-flow graph", fn))
			continue
		}
		for site, ops := range siteOps {
			if _, exists := known[site]; !exists {
				problems = append(problems, fmt.Sprintf("function %s publishes effects at site %v, which the graph does not contain", fn, site))
				continue
			}
			problems = append(problems, validateOps(fn, site, ops, source)...)
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

func validateOps(fn moduleid.FunctionID, site cfg.SiteID, ops []Op, source *thir.Module) []string {
	visitor := &validationVisitor{fn: fn, site: site, source: source}
	for index, op := range ops {
		visitor.index = index
		Visit(op, visitor)
	}
	for _, unclosed := range visitor.open {
		visitor.problems = append(visitor.problems, fmt.Sprintf("function %s site %v leaves call %d open", fn, site, unclosed))
	}
	return visitor.problems
}

type validationVisitor struct {
	fn       moduleid.FunctionID
	site     cfg.SiteID
	index    int
	source   *thir.Module
	problems []string
	open     []ir.NodeID
}

func (v *validationVisitor) where() string {
	return fmt.Sprintf("function %s site %v operation %d", v.fn, v.site, v.index)
}

func (v *validationVisitor) VisitDefine(op Define) {
	where := v.where()
	if op.Symbol == nil {
		v.problems = append(v.problems, where+" is a define with no symbol")
	}
	if op.IsOnEntry && op.Source == nil {
		matched := false
		if function := v.source.FunctionByID(v.fn); function != nil {
			for _, parameter := range function.Params {
				if parameter.Source.NodeID == op.Node && parameter.Symbol == op.Symbol {
					matched = true
					break
				}
			}
		}
		if !matched {
			v.problems = append(v.problems, fmt.Sprintf("%s is a define naming parameter %d not in typed THIR", where, op.Node))
		}
	} else {
		v.problems = append(v.problems, validateNode[thir.Node](where, "define", op.Node, op.Source, v.source)...)
	}
	if op.Value != 0 || op.ValueExpr != nil {
		v.problems = append(v.problems, validateNode[thir.Expr](where, "define value", op.Value, op.ValueExpr, v.source)...)
	}
}

func (v *validationVisitor) VisitWrite(op Write) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "write", op.Place, v.source)...)
	v.problems = append(v.problems, validateNode[thir.Expr](where, "write", op.Node, op.Target, v.source)...)
	v.problems = append(v.problems, validateNode[*thir.Assign](where, "write owner", op.Owner, v.source.Node(op.Owner), v.source)...)
	if op.Value != 0 || op.ValueExpr != nil {
		v.problems = append(v.problems, validateNode[thir.Expr](where, "write value", op.Value, op.ValueExpr, v.source)...)
	}
}

func (v *validationVisitor) VisitUse(op Use) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "use", op.Place, v.source)...)
	v.problems = append(v.problems, validateNode[thir.Expr](where, "use", op.Node, op.Source, v.source)...)
	if op.Location == nil {
		v.problems = append(v.problems, where+" is a use with no source location to report against")
	}
}

func (v *validationVisitor) VisitBorrow(op Borrow) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "borrow", op.Place, v.source)...)
	v.problems = append(v.problems, validateNode[thir.Expr](where, "borrow", op.Node, op.Source, v.source)...)
	v.problems = append(v.problems, validateNode[thir.Expr](where, "borrow operand", op.Operand, op.OperandExpr, v.source)...)
	if op.Location == nil {
		v.problems = append(v.problems, where+" is a borrow with no source location to report against")
	}
}

func (v *validationVisitor) VisitIterate(op Iterate) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "iteration", op.Place, v.source)...)
	v.problems = append(v.problems, validateNode[thir.Expr](where, "iteration", op.Node, op.Source, v.source)...)
	if op.Carrier == nil {
		v.problems = append(v.problems, where+" is an iteration with no symbol")
	}
	v.problems = append(v.problems, validateNode[*thir.For](where, "iteration owner", op.Loop, v.source.Node(op.Loop), v.source)...)
	if op.Location == nil {
		v.problems = append(v.problems, where+" is an iteration with no source location to report against")
	}
}

func (v *validationVisitor) VisitDiscard(op Discard) {
	where := v.where()
	v.problems = append(v.problems, validatePlace(where, "discard", op.Place, v.source)...)
	v.problems = append(v.problems, validateNode[thir.Expr](where, "discard", op.Node, op.Source, v.source)...)
	if op.Location == nil {
		v.problems = append(v.problems, where+" is a discard with no source location to report against")
	}
}

func (v *validationVisitor) VisitCallBegin(op CallBegin) {
	where := v.where()
	v.problems = append(v.problems, validateNode[*thir.Call](where, "call start", op.Node, op.Source, v.source)...)
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
func validatePlace(where, kind string, at Place, source *thir.Module) []string {
	switch {
	case at.Root == nil && at.Temporary == 0:
		return []string{fmt.Sprintf("%s is a %s whose place names neither a binding nor a temporary", where, kind)}
	case at.Root != nil && at.Temporary != 0:
		return []string{fmt.Sprintf("%s is a %s whose place names both binding %s and temporary %d",
			where, kind, at.Root.Name, at.Temporary)}
	case at.Temporary != 0:
		return validateNode[thir.Expr](where, kind+" temporary", at.Temporary, at.TemporaryExpr, source)
	}
	return nil
}

func validateNode[T thir.Node](where, kind string, node ir.NodeID, carried thir.Node, source *thir.Module) []string {
	indexed := source.Node(node)
	if indexed == nil {
		return []string{fmt.Sprintf("%s is a %s naming node %d, which is not in the typed THIR", where, kind, node)}
	}
	if _, ok := indexed.(T); !ok {
		return []string{fmt.Sprintf("%s is a %s naming node %d with unexpected node type %T", where, kind, node, indexed)}
	}
	if typednil.IsNil(carried) || carried != indexed {
		return []string{fmt.Sprintf("%s is a %s whose THIR source does not match node %d", where, kind, node)}
	}
	return nil
}

func graphSites(graph *cfg.ControlFlowGraph) map[cfg.SiteID]struct{} {
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
