package ownershipresult

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/bindingresult"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
)

// maxReportedProblems bounds one internal error so a systematic evidence break
// reports a readable sample instead of one line per node in the module.
const maxReportedProblems = 10

// Validate checks published ownership evidence against the artifacts it
// describes: every plan key must name a real program point, every published use
// kind must belong to a typed expression and be legal for that type's
// capability, and every call argument the typechecker resolved must carry a
// classification. A failure means the compiler published inconsistent evidence,
// so callers report it as an internal error, never as a source diagnostic.
//
// The validator never re-derives an ownership decision. In particular, proving
// that a symbol is dropped exactly once per path is deliberately out of scope:
// that is the analysis ownership already performs, and repeating it here would
// make the validator a second implementation of the thing it checks rather than
// a check on published shape.
func (r Result) Validate(types *typecheckresult.Result, bindings *bindingresult.Result, graphs *cfg.Module) error {
	if len(r) == 0 {
		return nil
	}
	if types == nil || bindings == nil || graphs == nil {
		return errors.New("ownership published a cleanup plan without typechecking, binding, or CFG evidence")
	}

	problems := validateValueUses(types)
	for fnID, plan := range r {
		problems = append(problems, validatePlan(fnID, plan, types, bindings, graphs)...)
	}
	if len(problems) == 0 {
		return nil
	}
	// Plans and evidence are maps, so a stable report needs an explicit order.
	sort.Strings(problems)
	total := len(problems)
	if len(problems) > maxReportedProblems {
		problems = problems[:maxReportedProblems]
		return fmt.Errorf("%s (%d more)", strings.Join(problems, "; "), total-maxReportedProblems)
	}
	return errors.New(strings.Join(problems, "; "))
}

// validateValueUses checks the published use kinds against the expressions they
// classify: a kind for an untyped node is stale evidence, a kind the type's
// capability forbids is an illegal classification, and a resolved call argument
// with no kind is the gap the ownership fallback used to hide.
func validateValueUses(types *typecheckresult.Result) []string {
	problems := make([]string, 0)
	for id, use := range types.ValueUses {
		valueType, typed := types.ExprTypes[id]
		if !typed {
			problems = append(problems, fmt.Sprintf("use kind published for node %d with no expression type", id))
			continue
		}
		if use == typeinfo.UseCopy && typeinfo.OwnershipCapabilityOf(valueType).Copy == typeinfo.CopyNever {
			problems = append(problems, fmt.Sprintf("node %d copies %s, which has no copy operation", id, typeinfo.TypeText(valueType)))
		}
	}
	for callID, args := range types.EffectiveCallArguments {
		for index, arg := range args {
			if arg == nil {
				continue
			}
			if _, published := types.ValueUses[arg.ID()]; !published {
				problems = append(problems, fmt.Sprintf("call %d argument %d has no published use kind", callID, index))
			}
		}
	}
	return problems
}

// validatePlan checks one function's cleanup plan against its CFG and the
// program points each map is keyed by.
func validatePlan(fnID ir.NodeID, plan *CleanupPlan, types *typecheckresult.Result, bindings *bindingresult.Result, graphs *cfg.Module) []string {
	if plan == nil {
		return []string{fmt.Sprintf("function %d has a nil cleanup plan", fnID)}
	}
	graph := graphs.Function(fnID)
	if graph == nil {
		return []string{fmt.Sprintf("function %d has a cleanup plan but no CFG", fnID)}
	}

	scopeExits := make(map[cfg.SiteID]struct{})
	siteNodes := make(map[ir.NodeID]struct{})
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
			problems = append(problems, fmt.Sprintf("function %d drops at site %v, which is not a scope exit in its CFG", fnID, siteID))
		}
		problems = append(problems, validateSymbols(fnID, "scope exit", ids)...)
	}
	for nodeID, ids := range plan.BeforeReturn {
		if _, exists := siteNodes[nodeID]; !exists {
			problems = append(problems, fmt.Sprintf("function %d drops before return %d, which is not a site in its CFG", fnID, nodeID))
		}
		problems = append(problems, validateSymbols(fnID, "return", ids)...)
	}
	for nodeID := range plan.BeforeAssign {
		if _, exists := siteNodes[nodeID]; !exists {
			problems = append(problems, fmt.Sprintf("function %d drops before assignment %d, which is not a site in its CFG", fnID, nodeID))
		}
	}
	for nodeID := range plan.DiscardedValue {
		problems = append(problems, validateTypedNode(types, fnID, "discarded value", nodeID)...)
	}
	for nodeID := range plan.ProjectionBase {
		problems = append(problems, validateTypedNode(types, fnID, "projection base", nodeID)...)
	}
	for nodeID, symbolID := range plan.MatchCarrierMoves {
		problems = append(problems, validateArmBody(bindings, fnID, "match carrier move", nodeID)...)
		if symbolID == 0 {
			problems = append(problems, fmt.Sprintf("function %d moves an unidentified match carrier at %d", fnID, nodeID))
		}
	}
	for nodeID := range plan.MatchWholePayloadDrops {
		problems = append(problems, validateArmBody(bindings, fnID, "match payload drop", nodeID)...)
	}
	for nodeID, fields := range plan.MatchFieldDrops {
		problems = append(problems, validateArmBody(bindings, fnID, "match field drop", nodeID)...)
		for _, field := range fields {
			if field < 0 {
				problems = append(problems, fmt.Sprintf("function %d drops match field %d at %d", fnID, field, nodeID))
			}
		}
	}
	return problems
}

// validateSymbols rejects unidentified cleanup targets. Full symbol-identity
// checking waits for a canonical symbol registry; a zero id is already proof the
// plan lost the symbol it meant to drop.
func validateSymbols(fnID ir.NodeID, where string, ids []symbols.SymbolID) []string {
	problems := make([]string, 0)
	for _, id := range ids {
		if id == 0 {
			problems = append(problems, fmt.Sprintf("function %d plans an unidentified %s drop", fnID, where))
		}
	}
	return problems
}

func validateTypedNode(types *typecheckresult.Result, fnID ir.NodeID, where string, nodeID ir.NodeID) []string {
	if _, typed := types.ExprTypes[ast.NodeID(nodeID)]; typed {
		return nil
	}
	return []string{fmt.Sprintf("function %d plans a %s at node %d with no expression type", fnID, where, nodeID)}
}

func validateArmBody(bindings *bindingresult.Result, fnID ir.NodeID, where string, nodeID ir.NodeID) []string {
	if _, scoped := bindings.BlockScopes[ast.NodeID(nodeID)]; scoped {
		return nil
	}
	return []string{fmt.Sprintf("function %d plans a %s at node %d, which is not a block", fnID, where, nodeID)}
}
