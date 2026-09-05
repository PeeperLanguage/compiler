// Package typecheckresult defines semantic evidence produced by base typechecking.
package typecheckresult

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
)

// InterfaceImplementation proves that one declared method materializes an interface slot.
type InterfaceImplementation struct {
	Symbol       *symbols.Symbol
	CallableType *typeinfo.FuncType
}

type CaseTest struct {
	SubjectID    ast.NodeID
	Case         int
	CaseWhenTrue bool
	CaseCount    int
	Family       typeinfo.VariantFamily
}

// Match records typechecker-owned case and binding evidence consumed by CFG
// and later semantic phases without resolving source paths again.
type Match struct {
	SubjectID ast.NodeID
	EnumType  typeinfo.Type
	CaseCount int
	Arms      []MatchArm
}

type MatchProjection uint8

const (
	MatchProjectionInvalid MatchProjection = iota
	MatchPayloadField
	MatchWholePayload
)

type MatchArm struct {
	ArmID    ast.NodeID
	BodyID   ast.NodeID
	Case     int
	Payload  typeinfo.Type
	Bindings []MatchBinding
	// CarrierUse is the published use kind applied to the match subject
	// carrier when this arm is selected: UseMove when the arm binds any
	// move-only payload part, UseRead when everything binds by copy.
	CarrierUse typeinfo.UseKind
}

type MatchBinding struct {
	Projection MatchProjection
	Field      int
	Type       typeinfo.Type
	Binding    *symbols.Symbol
	Discard    bool
}

func (m Match) Arm(caseIndex int) (MatchArm, bool) {
	for _, arm := range m.Arms {
		if arm.Case == caseIndex {
			return arm, true
		}
	}
	return MatchArm{}, false
}

// ForIteration records typechecker-owned loop lowering and CFG evidence.
// Generated symbols carry hidden loop state; source bindings remain body-scoped.
//
// Evidence is published only for a loop that typed cleanly, so a record that
// exists is complete: Cursor, Value and ElementType are always set, and Plan
// carries exactly one iteration kind. Consumers switch on the plan and read its
// fields; they must not re-check them, because a nil there would be a compiler
// bug rather than a shape the source can produce.
type ForIteration struct {
	GuaranteedEntry bool

	ElementType typeinfo.Type
	Cursor      *symbols.Symbol
	Value       *symbols.Symbol
	// Index is nil unless the source binds an index name.
	Index *symbols.Symbol

	// Plan carries the iteration kind and that kind's state as one value.
	// There is no separate kind tag to disagree with the state, and no way to
	// hold both kinds at once.
	Plan IterationPlan
}

// IterationPlan is the state one iteration kind needs. The interface is closed
// by its unexported method: only the two plans below implement it, so a
// consumer's type switch over both is exhaustive and a third kind cannot be
// introduced outside this package.
type IterationPlan interface {
	iterationPlan()
}

// RangeIteration is the state of a `for i in a..b` loop. Limit holds the
// evaluated exclusive upper bound; Ordinal counts iterations and is non-nil
// exactly when ForIteration.Index is.
type RangeIteration struct {
	Limit   *symbols.Symbol
	Ordinal *symbols.Symbol
}

func (*RangeIteration) iterationPlan() {}

// SequenceIteration is the state of a `for v in seq` loop. Carrier holds the
// iterated storage for the loop's lifetime, borrowed when CarrierType is a
// reference; the cursor indexes through it.
type SequenceIteration struct {
	Carrier     *symbols.Symbol
	CarrierType typeinfo.Type
}

func (*SequenceIteration) iterationPlan() {}

// VariantConstruction records resolved enum construction without later path or field resolution.
type VariantConstruction struct {
	EnumType typeinfo.Type
	Case     int
	Payload  typeinfo.Type
	Value    ast.Expr
}

// CompilerCall records intrinsic dispatch selected by typechecking.
type CompilerCall struct {
	Operation symbols.CompilerOp
	Kind      intrinsics.FunctionKind
}

// Result owns base semantic evidence for one typecheck generation.
type Result struct {
	ExpandedDefaultBindings  map[ast.NodeID]struct{}
	EffectiveCallArguments   map[ast.NodeID][]ast.Expr
	InterfaceImplementations map[ast.NodeID][]InterfaceImplementation
	ImplicitConversions      map[ast.NodeID]typeinfo.Conversion
	ImplicitCallArguments    map[ast.NodeID]typeinfo.Type
	CompilerCalls            map[ast.NodeID]CompilerCall
	StringConcatenations     map[ast.NodeID]struct{}
	VariantConstructions     map[ast.NodeID]VariantConstruction
	CaseTests                map[ast.NodeID]CaseTest
	Matches                  map[ast.NodeID]Match
	ForIterations            map[ast.NodeID]ForIteration
	ExprTypes                map[ast.NodeID]typeinfo.Type
	// ValueUses classifies every ownership-relevant value use, keyed by the
	// used expression's node ID. Reference parameters publish UseRead; the
	// borrow machinery in ownership still governs them.
	ValueUses map[ast.NodeID]typeinfo.UseKind
	// ReferenceArguments records an argument whose parameter is a reference.
	// Presence is the fact; the value reports whether that reference is mutable,
	// which separates a shared borrow from a mutable reservation.
	//
	// The borrow follows from the parameter type, so it is invisible in the
	// argument: passing a reference-typed value to a reference parameter writes
	// no ampersand and produces no address expression.
	ReferenceArguments map[ast.NodeID]bool
}

func New() *Result {
	return &Result{
		ExpandedDefaultBindings:  make(map[ast.NodeID]struct{}),
		EffectiveCallArguments:   make(map[ast.NodeID][]ast.Expr),
		InterfaceImplementations: make(map[ast.NodeID][]InterfaceImplementation),
		ImplicitConversions:      make(map[ast.NodeID]typeinfo.Conversion),
		ImplicitCallArguments:    make(map[ast.NodeID]typeinfo.Type),
		CompilerCalls:            make(map[ast.NodeID]CompilerCall),
		StringConcatenations:     make(map[ast.NodeID]struct{}),
		VariantConstructions:     make(map[ast.NodeID]VariantConstruction),
		CaseTests:                make(map[ast.NodeID]CaseTest),
		Matches:                  make(map[ast.NodeID]Match),
		ForIterations:            make(map[ast.NodeID]ForIteration),
		ExprTypes:                make(map[ast.NodeID]typeinfo.Type),
		ValueUses:                make(map[ast.NodeID]typeinfo.UseKind),
		ReferenceArguments:       make(map[ast.NodeID]bool),
	}
}

// MatchCases exposes resolved case indexes without leaking match artifacts
// into CFG's source-topology package.
func (r *Result) MatchCases(id ast.NodeID) ([]int, bool) {
	if r == nil {
		return nil, false
	}
	match, found := r.Matches[id]
	if !found {
		return nil, false
	}
	cases := make([]int, len(match.Arms))
	for index, arm := range match.Arms {
		if arm.Case < 0 || arm.Case >= match.CaseCount {
			return nil, false
		}
		cases[index] = arm.Case
	}
	return cases, true
}

// StringConcatenation reports whether a binary expression was resolved as a
// string concatenation, which consumes its left operand.
func (r *Result) StringConcatenation(id ast.NodeID) bool {
	if r == nil {
		return false
	}
	_, found := r.StringConcatenations[id]
	return found
}

// ValueUse exposes the use kind the typechecker decided for one expression.
// Coverage is call arguments today, so an absent answer is normal rather than
// a missing decision.
func (r *Result) ValueUse(id ast.NodeID) (typeinfo.UseKind, bool) {
	if r == nil {
		return typeinfo.UseRead, false
	}
	kind, found := r.ValueUses[id]
	return kind, found
}

// ReferenceArgument reports whether an argument's parameter is a reference and,
// when it is, whether that reference is mutable.
func (r *Result) ReferenceArgument(id ast.NodeID) (mutable bool, found bool) {
	if r == nil {
		return false, false
	}
	mutable, found = r.ReferenceArguments[id]
	return mutable, found
}

// ArmBindings exposes the payload symbols one match arm binds, without leaking
// match artifacts into the effect producer. A discarded binding still binds
// storage, so it is reported like any other.
func (r *Result) ArmBindings(match ast.NodeID, caseIndex int) []*symbols.Symbol {
	if r == nil {
		return nil
	}
	evidence, found := r.Matches[match]
	if !found {
		return nil
	}
	arm, found := evidence.Arm(caseIndex)
	if !found {
		return nil
	}
	bound := make([]*symbols.Symbol, 0, len(arm.Bindings))
	for _, binding := range arm.Bindings {
		if binding.Binding != nil {
			bound = append(bound, binding.Binding)
		}
	}
	return bound
}

// ForLoopGuaranteedEntry exposes typechecker proof that one loop executes its
// body before its first condition check.
func (r *Result) ForLoopGuaranteedEntry(id ast.NodeID) bool {
	if r == nil {
		return false
	}
	iteration, found := r.ForIterations[id]
	return found && iteration.GuaranteedEntry
}

// SequenceCarrier exposes the hidden carrier a typed sequence loop keeps for
// the loop lifetime. Range loops have no carrier. Consumers ask this query
// instead of inspecting the concrete iteration plan themselves.
func (r *Result) SequenceCarrier(id ast.NodeID) (*symbols.Symbol, bool) {
	if r == nil {
		return nil, false
	}
	iteration, found := r.ForIterations[id]
	if !found {
		return nil, false
	}
	sequence, ok := iteration.Plan.(*SequenceIteration)
	if !ok || sequence == nil || sequence.Carrier == nil {
		return nil, false
	}
	return sequence.Carrier, true
}

// CallArgumentsOrSource returns published effective arguments when available.
// Semantic phases that continue after diagnostics use source arguments when
// typechecking could not publish complete call evidence.
func (r *Result) CallArgumentsOrSource(call *ast.CallExpr) []ast.Expr {
	if call == nil {
		return nil
	}
	if r != nil {
		if args, found := r.EffectiveCallArguments[call.ID()]; found {
			return args
		}
	}
	return call.Args
}
