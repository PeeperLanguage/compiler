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

type ForIterationKind uint8

const (
	ForIterationRange ForIterationKind = iota
	ForIterationSequence
)

// ForIteration records typechecker-owned loop lowering and CFG evidence.
// Generated symbols carry hidden loop state; source bindings remain body-scoped.
type ForIteration struct {
	Kind            ForIterationKind
	GuaranteedEntry bool

	ElementType typeinfo.Type
	CarrierType typeinfo.Type
	Carrier     *symbols.Symbol
	Cursor      *symbols.Symbol
	End         *symbols.Symbol
	Ordinal     *symbols.Symbol
	Index       *symbols.Symbol
	Value       *symbols.Symbol
}

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
