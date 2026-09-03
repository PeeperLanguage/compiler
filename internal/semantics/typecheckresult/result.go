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

// ForLoopGuaranteedEntry exposes typechecker proof that one loop executes its
// body before its first condition check.
func (r *Result) ForLoopGuaranteedEntry(id ast.NodeID) bool {
	if r == nil {
		return false
	}
	iteration, found := r.ForIterations[id]
	return found && iteration.GuaranteedEntry
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
