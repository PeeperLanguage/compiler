package analysis

import (
	"compiler/internal/source"
	"errors"
	"fmt"
	"sort"
	"strings"

	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
	"compiler/internal/moduleid"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// maxAnalysisValidationProblems bounds one internal error so a systematic evidence break
// reports a readable sample instead of one line per node in the module.
const maxAnalysisValidationProblems = 10

func (m *Module) validate(source *thir.Module, symbolIndex *symbols.Index, graphs *cfg.Module) error {
	if m == nil {
		return errors.New("analysis produced no artifact")
	}
	if err := validationError(validateExpressionEvidence(m.expressions, source)); err != nil {
		return err
	}
	return m.cleanup.validate(source, symbolIndex, graphs)
}

func validateExpressionEvidence(evidence expressionEvidence, source *thir.Module) []string {
	problems := make([]string, 0)
	for id, typ := range evidence.types {
		problems = append(problems, validateExpressionNode(source, id, "type refinement")...)
		if typ == nil {
			problems = append(problems, fmt.Sprintf("node %v has a nil type refinement", id))
		}
	}
	for id, conversion := range evidence.conversions {
		problems = append(problems, validateExpressionNode(source, id, "implicit conversion")...)
		if conversion.Compatibility != typeinfo.Compatible || conversion.Kind == typeinfo.ConversionIdentity || conversion.Kind == typeinfo.ConversionNone {
			problems = append(problems, fmt.Sprintf("node %v has non-materializing implicit conversion %v", id, conversion.Kind))
		}
	}
	for id, payload := range evidence.payloads {
		problems = append(problems, validateExpressionNode(source, id, "payload refinement")...)
		if len(payload.Cases) == 0 {
			problems = append(problems, fmt.Sprintf("node %v has an empty payload refinement", id))
		}
		for _, caseIndex := range payload.Cases {
			if caseIndex < 0 {
				problems = append(problems, fmt.Sprintf("node %v has negative payload case %v", id, caseIndex))
			}
		}
	}
	for id, test := range evidence.caseTests {
		problems = append(problems, validateExpressionNode(source, id, "case test")...)
		problems = append(problems, validateExpressionNode(source, test.SubjectID, "case-test subject")...)
		if test.CaseCount <= 0 || test.Case < 0 || test.Case >= test.CaseCount {
			problems = append(problems, fmt.Sprintf("node %v tests case %v of %v", id, test.Case, test.CaseCount))
		}
		for _, caseIndex := range test.PayloadPath {
			if caseIndex < 0 {
				problems = append(problems, fmt.Sprintf("node %v has negative payload-path case %v", id, caseIndex))
			}
		}
	}
	for id, field := range evidence.variantFields {
		problems = append(problems, validateExpressionNode(source, id, "variant field")...)
		problems = append(problems, validateExpressionNode(source, field.Carrier, "variant-field carrier")...)
		if field.Case < 0 {
			problems = append(problems, fmt.Sprintf("node %v selects negative variant case %v", id, field.Case))
		}
		if field.Payload == nil || field.Field < 0 || field.Field >= len(field.Payload.Fields) {
			problems = append(problems, fmt.Sprintf("node %v selects invalid variant field %v", id, field.Field))
		}
		if field.Type == nil {
			problems = append(problems, fmt.Sprintf("node %v has a nil variant-field type", id))
		}
	}
	for id := range evidence.origins {
		if !id.IsValid() {
			problems = append(problems, "origin resolution published with no source identity")
		}
	}
	for id, slots := range evidence.aggregates {
		problems = append(problems, validateExpressionNode(source, id, "aggregate slots")...)
		for index, slot := range slots {
			if slot.ValueExpr == nil {
				problems = append(problems, fmt.Sprintf("node %v aggregate slot %v has no value expression", id, index))
			}
		}
	}
	return problems
}

func validateExpressionNode(typed *thir.Module, id source.NodeID, fact string) []string {
	if typed != nil {
		if expr, ok := typed.Node(id).(thir.Expr); ok && expr != nil {
			return nil
		}
	}
	return []string{fmt.Sprintf("%s published for node %v with no typed expression", fact, id)}
}

func validationError(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	total := len(problems)
	if len(problems) > maxAnalysisValidationProblems {
		problems = problems[:maxAnalysisValidationProblems]
		return fmt.Errorf("%s (%v more)", strings.Join(problems, "; "), total-maxAnalysisValidationProblems)
	}
	return errors.New(strings.Join(problems, "; "))
}

// validate checks ownership evidence against the artifacts it describes: every
// plan key must name a real program point, every recorded use
// kind must belong to a typed expression and be legal for that type's
// capability, and every call argument the typechecker resolved must carry a
// classification. A failure means the compiler derived inconsistent evidence,
// so callers report it as an internal error, never as a source diagnostic.
//
// The validator never re-derives an ownership decision. In particular, proving
// that a symbol is dropped exactly once per path is deliberately out of scope:
// that is the analysis ownership already performs, and repeating it here would
// make the validator a second implementation of the thing it checks rather than
// a check on stored shape.
func (r cleanupPlans) validate(source *thir.Module, symbolIndex *symbols.Index, graphs *cfg.Module) error {
	if len(r) == 0 && graphs == nil {
		return nil
	}
	if source == nil || symbolIndex == nil || graphs == nil {
		return errors.New("ownership published a cleanup plan without typechecking, binding, or CFG evidence")
	}

	problems := make([]string, 0)
	for _, graph := range graphs.Functions {
		if graph == nil {
			continue
		}
		if _, found := r[graph.FunctionID]; !found {
			problems = append(problems, fmt.Sprintf("function %s has a control-flow graph but no published cleanup plan", graph.FunctionID))
		}
	}
	problems = append(problems, validateValueUses(source)...)
	for fnID, plan := range r {
		problems = append(problems, validatePlan(fnID, plan, source, symbolIndex, graphs)...)
	}
	return validationError(problems)
}

// validateValueUses checks the recorded use kinds against the expressions they
// classify: a kind for an untyped node is stale evidence, a kind the type's
// capability forbids is an illegal classification, and a resolved call argument
// with no kind is the gap the ownership fallback used to hide.
func validateValueUses(source *thir.Module) []string {
	problems := make([]string, 0)
	if source == nil {
		return problems
	}
	for _, function := range source.Functions {
		if function == nil || function.Body == nil {
			continue
		}
		thir.Inspect(function.Body, func(node thir.Node) bool {
			expr, ok := node.(thir.Expr)
			if ok && expr != nil {
				if use, published := expr.UseKind(); published {
					valueType := expr.ExprType()
					if valueType == nil {
						problems = append(problems, fmt.Sprintf("use kind published for node %v with no expression type", expr.SourceInfo().NodeID))
					} else if use == typeinfo.UseCopy && typeinfo.OwnershipCapabilityOf(valueType).Copy == typeinfo.CopyNever {
						problems = append(problems, fmt.Sprintf("node %v copies %s, which has no copy operation", expr.SourceInfo().NodeID, typeinfo.TypeText(valueType)))
					}
				}
			}
			call, ok := node.(*thir.Call)
			if !ok || call == nil {
				return true
			}
			for index, arg := range call.Args {
				if arg == nil {
					continue
				}
				if _, published := arg.UseKind(); !published {
					problems = append(problems, fmt.Sprintf("call %v argument %v has no published use kind", call.SourceInfo().NodeID, index))
				}
			}
			return true
		})
	}
	return problems
}

// validatePlan checks one function's cleanup plan against its CFG and the
// program points each map is keyed by.
func validatePlan(fnID moduleid.FunctionID, plan *cleanupPlan, typed *thir.Module, symbolIndex *symbols.Index, graphs *cfg.Module) []string {
	if plan == nil {
		return []string{fmt.Sprintf("function %s has a nil cleanup plan", fnID)}
	}
	graph := graphs.FunctionByID(fnID)
	if graph == nil {
		return []string{fmt.Sprintf("function %s has a cleanup plan but no CFG", fnID)}
	}

	scopeExits := make(map[cfg.SiteID]struct{})
	siteNodes := make(map[source.NodeID]struct{})
	for _, block := range graph.Blocks {
		if block == nil {
			continue
		}
		for _, site := range block.Sites {
			if site == nil {
				continue
			}
			siteNodes[site.NodeID] = struct{}{}
			if site.Kind == cfg.SiteScopeExit {
				scopeExits[site.ID] = struct{}{}
			}
		}
	}

	problems := make([]string, 0)
	for siteID, ids := range plan.AfterScope {
		if _, exists := scopeExits[siteID]; !exists {
			problems = append(problems, fmt.Sprintf("function %s drops at site %v, which is not a scope exit in its CFG", fnID, siteID))
		}
		problems = append(problems, validateSymbols(fnID, "scope exit", ids)...)
	}
	for nodeID, ids := range plan.BeforeReturn {
		if _, exists := siteNodes[nodeID]; !exists {
			problems = append(problems, fmt.Sprintf("function %s drops before return %v, which is not a site in its CFG", fnID, nodeID))
		}
		problems = append(problems, validateSymbols(fnID, "return", ids)...)
	}
	for nodeID := range plan.BeforeAssign {
		if _, exists := siteNodes[nodeID]; !exists {
			problems = append(problems, fmt.Sprintf("function %s drops before assignment %v, which is not a site in its CFG", fnID, nodeID))
		}
	}
	for nodeID := range plan.DiscardedValue {
		problems = append(problems, validateTypedNode(typed, fnID, "discarded value", nodeID)...)
	}
	for nodeID := range plan.ProjectionBase {
		problems = append(problems, validateTypedNode(typed, fnID, "projection base", nodeID)...)
	}
	for nodeID := range plan.MatchWholePayloadDrops {
		problems = append(problems, validateArmBody(symbolIndex, fnID, "match payload drop", nodeID)...)
	}
	for nodeID, fields := range plan.MatchFieldDrops {
		problems = append(problems, validateArmBody(symbolIndex, fnID, "match field drop", nodeID)...)
		for _, field := range fields {
			if field < 0 {
				problems = append(problems, fmt.Sprintf("function %s drops match field %v at %v", fnID, field, nodeID))
			}
		}
	}
	return problems
}

// validateSymbols rejects unidentified cleanup targets. Full symbol-identity
// checking waits for a canonical symbol registry; an invalid ID is already proof
// the plan lost the symbol it meant to drop.
func validateSymbols(fnID moduleid.FunctionID, where string, ids []symbols.SymbolID) []string {
	problems := make([]string, 0)
	for _, id := range ids {
		if !id.IsValid() {
			problems = append(problems, fmt.Sprintf("function %s plans an unidentified %s drop", fnID, where))
		}
	}
	return problems
}

func validateTypedNode(typed *thir.Module, fnID moduleid.FunctionID, where string, nodeID source.NodeID) []string {
	if typed != nil {
		if expr, ok := typed.Node(nodeID).(thir.Expr); ok && expr != nil && expr.ExprType() != nil {
			return nil
		}
	}
	return []string{fmt.Sprintf("function %s plans a %s at node %v with no expression type", fnID, where, nodeID)}
}

func validateArmBody(symbolIndex *symbols.Index, fnID moduleid.FunctionID, where string, nodeID source.NodeID) []string {
	if symbolIndex.ScopeID(nodeID) != nil {
		return nil
	}
	return []string{fmt.Sprintf("function %s plans a %s at node %v, which is not a block", fnID, where, nodeID)}
}
