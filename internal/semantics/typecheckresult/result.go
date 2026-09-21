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

// ConstantIndex records the typechecked value of a fixed-array index so lowering
// does not invoke constant evaluation again.
type ConstantIndex struct {
	Text string
	Type typeinfo.Type
}

// StructFieldAccess records ordinary field selection after pointer/reference normalization.
type StructFieldAccess struct {
	Field           int
	Type            typeinfo.Type
	DereferenceType typeinfo.Type
}

// CompilerCall records intrinsic dispatch selected by typechecking.
type CompilerCall struct {
	Operation symbols.CompilerOp
	Kind      intrinsics.FunctionKind
}

// Result owns base semantic evidence for one typecheck generation. Backing
// indexes are grouped by semantic domain and remain private so producers and
// consumers depend on compiler operations rather than storage layout.
type Result struct {
	expressions expressionEvidence
	calls       callEvidence
	control     controlEvidence
}

type expressionEvidence struct {
	types                    map[ast.NodeID]typeinfo.Type
	expandedDefaultBindings  map[ast.NodeID]struct{}
	interfaceImplementations map[ast.NodeID][]InterfaceImplementation
	implicitConversions      map[ast.NodeID]typeinfo.Conversion
	stringConcatenations     map[ast.NodeID]struct{}
	structFields             map[ast.NodeID]StructFieldAccess
	constantIndexes          map[ast.NodeID]ConstantIndex
	structLiteralFields      map[ast.NodeID][]ast.Expr
	variantConstructions     map[ast.NodeID]VariantConstruction
	valueUses                map[ast.NodeID]typeinfo.UseKind
	referenceArguments       map[ast.NodeID]bool
}

type callEvidence struct {
	effectiveArguments map[ast.NodeID][]ast.Expr
	implicitArguments  map[ast.NodeID]typeinfo.Type
	compilerCalls      map[ast.NodeID]CompilerCall
}

type controlEvidence struct {
	caseTests         map[ast.NodeID]CaseTest
	matches           map[ast.NodeID]Match
	forIterations     map[ast.NodeID]ForIteration
	checkedIterations map[ast.NodeID]*ast.BlockStmt
}

func New() *Result {
	return &Result{
		expressions: expressionEvidence{
			types:                    make(map[ast.NodeID]typeinfo.Type),
			expandedDefaultBindings:  make(map[ast.NodeID]struct{}),
			interfaceImplementations: make(map[ast.NodeID][]InterfaceImplementation),
			implicitConversions:      make(map[ast.NodeID]typeinfo.Conversion),
			stringConcatenations:     make(map[ast.NodeID]struct{}),
			structFields:             make(map[ast.NodeID]StructFieldAccess),
			constantIndexes:          make(map[ast.NodeID]ConstantIndex),
			structLiteralFields:      make(map[ast.NodeID][]ast.Expr),
			variantConstructions:     make(map[ast.NodeID]VariantConstruction),
			valueUses:                make(map[ast.NodeID]typeinfo.UseKind),
			referenceArguments:       make(map[ast.NodeID]bool),
		},
		calls: callEvidence{
			effectiveArguments: make(map[ast.NodeID][]ast.Expr),
			implicitArguments:  make(map[ast.NodeID]typeinfo.Type),
			compilerCalls:      make(map[ast.NodeID]CompilerCall),
		},
		control: controlEvidence{
			caseTests:         make(map[ast.NodeID]CaseTest),
			matches:           make(map[ast.NodeID]Match),
			forIterations:     make(map[ast.NodeID]ForIteration),
			checkedIterations: make(map[ast.NodeID]*ast.BlockStmt),
		},
	}
}

func (r *Result) RecordExprType(id ast.NodeID, typ typeinfo.Type) {
	if r == nil || id == 0 || typ == nil {
		return
	}
	r.expressions.types[id] = typ
}

func (r *Result) ExprType(id ast.NodeID) typeinfo.Type {
	if r == nil || id == 0 {
		return nil
	}
	return r.expressions.types[id]
}

func (r *Result) MarkExpandedDefaultBinding(id ast.NodeID) {
	if r != nil && id != 0 {
		r.expressions.expandedDefaultBindings[id] = struct{}{}
	}
}

func (r *Result) ExpandedDefaultBinding(id ast.NodeID) bool {
	if r == nil || id == 0 {
		return false
	}
	_, ok := r.expressions.expandedDefaultBindings[id]
	return ok
}

func (r *Result) RecordInterfaceImplementations(id ast.NodeID, implementations []InterfaceImplementation) {
	if r == nil || id == 0 || len(implementations) == 0 {
		return
	}
	r.expressions.interfaceImplementations[id] = implementations
}

func (r *Result) InterfaceImplementations(id ast.NodeID) []InterfaceImplementation {
	if r == nil || id == 0 {
		return nil
	}
	return r.expressions.interfaceImplementations[id]
}

func (r *Result) RecordImplicitConversion(id ast.NodeID, conversion typeinfo.Conversion) {
	if r != nil && id != 0 {
		r.expressions.implicitConversions[id] = conversion
	}
}

func (r *Result) ImplicitConversion(id ast.NodeID) (typeinfo.Conversion, bool) {
	if r == nil || id == 0 {
		return typeinfo.Conversion{}, false
	}
	conversion, ok := r.expressions.implicitConversions[id]
	return conversion, ok
}

func (r *Result) MarkStringConcatenation(id ast.NodeID) {
	if r != nil && id != 0 {
		r.expressions.stringConcatenations[id] = struct{}{}
	}
}

// StringConcatenation reports whether a binary expression was resolved as a
// string concatenation, which consumes its left operand.
func (r *Result) StringConcatenation(id ast.NodeID) bool {
	if r == nil || id == 0 {
		return false
	}
	_, found := r.expressions.stringConcatenations[id]
	return found
}

func (r *Result) RecordStructField(id ast.NodeID, access StructFieldAccess) {
	if r != nil && id != 0 {
		r.expressions.structFields[id] = access
	}
}

func (r *Result) StructField(id ast.NodeID) (StructFieldAccess, bool) {
	if r == nil || id == 0 {
		return StructFieldAccess{}, false
	}
	access, ok := r.expressions.structFields[id]
	return access, ok
}

func (r *Result) RecordConstantIndex(id ast.NodeID, value ConstantIndex) {
	if r == nil || id == 0 || value.Text == "" || value.Type == nil {
		return
	}
	r.expressions.constantIndexes[id] = value
}

func (r *Result) ConstantIndex(id ast.NodeID) (ConstantIndex, bool) {
	if r == nil || id == 0 {
		return ConstantIndex{}, false
	}
	value, ok := r.expressions.constantIndexes[id]
	return value, ok
}

func (r *Result) RecordStructLiteralFields(id ast.NodeID, fields []ast.Expr) {
	if r == nil || id == 0 || fields == nil {
		return
	}
	r.expressions.structLiteralFields[id] = append([]ast.Expr(nil), fields...)
}

func (r *Result) StructLiteralFields(id ast.NodeID) ([]ast.Expr, bool) {
	if r == nil || id == 0 {
		return nil, false
	}
	fields, ok := r.expressions.structLiteralFields[id]
	return append([]ast.Expr(nil), fields...), ok
}

func (r *Result) RecordVariantConstruction(id ast.NodeID, construction VariantConstruction) {
	if r != nil && id != 0 {
		r.expressions.variantConstructions[id] = construction
	}
}

func (r *Result) VariantConstruction(id ast.NodeID) (VariantConstruction, bool) {
	if r == nil || id == 0 {
		return VariantConstruction{}, false
	}
	construction, ok := r.expressions.variantConstructions[id]
	return construction, ok
}

func (r *Result) RecordValueUse(id ast.NodeID, use typeinfo.UseKind) {
	if r != nil && id != 0 {
		r.expressions.valueUses[id] = use
	}
}

// ValueUse exposes the use kind the typechecker decided for one expression.
// Coverage is call arguments today, so an absent answer is normal rather than
// a missing decision.
func (r *Result) ValueUse(id ast.NodeID) (typeinfo.UseKind, bool) {
	if r == nil || id == 0 {
		return typeinfo.UseRead, false
	}
	kind, found := r.expressions.valueUses[id]
	return kind, found
}

func (r *Result) ForEachValueUse(fn func(ast.NodeID, typeinfo.UseKind)) {
	if r == nil || fn == nil {
		return
	}
	for id, use := range r.expressions.valueUses {
		fn(id, use)
	}
}

func (r *Result) RecordReferenceArgument(id ast.NodeID, mutable bool) {
	if r != nil && id != 0 {
		r.expressions.referenceArguments[id] = mutable
	}
}

// ReferenceArgument reports whether an argument's parameter is a reference and,
// when it is, whether that reference is mutable.
func (r *Result) ReferenceArgument(id ast.NodeID) (mutable bool, found bool) {
	if r == nil || id == 0 {
		return false, false
	}
	mutable, found = r.expressions.referenceArguments[id]
	return mutable, found
}

func (r *Result) RecordCallArguments(id ast.NodeID, args []ast.Expr) {
	if r == nil || id == 0 {
		return
	}
	r.calls.effectiveArguments[id] = args
}

func (r *Result) CallArguments(id ast.NodeID) ([]ast.Expr, bool) {
	if r == nil || id == 0 {
		return nil, false
	}
	args, ok := r.calls.effectiveArguments[id]
	return args, ok
}

func (r *Result) ForEachCallArguments(fn func(ast.NodeID, []ast.Expr)) {
	if r == nil || fn == nil {
		return
	}
	for id, args := range r.calls.effectiveArguments {
		fn(id, args)
	}
}

// CallArgumentsOrSource returns published effective arguments when available.
// Semantic phases that continue after diagnostics use source arguments when
// typechecking could not publish complete call evidence.
func (r *Result) CallArgumentsOrSource(call *ast.CallExpr) []ast.Expr {
	if call == nil {
		return nil
	}
	if args, found := r.CallArguments(call.ID()); found {
		return args
	}
	return call.Args
}

func (r *Result) RecordImplicitCallArgument(id ast.NodeID, typ typeinfo.Type) {
	if r != nil && id != 0 && typ != nil {
		r.calls.implicitArguments[id] = typ
	}
}

func (r *Result) ImplicitCallArgument(id ast.NodeID) typeinfo.Type {
	if r == nil || id == 0 {
		return nil
	}
	return r.calls.implicitArguments[id]
}

func (r *Result) RecordCompilerCall(id ast.NodeID, call CompilerCall) {
	if r != nil && id != 0 {
		r.calls.compilerCalls[id] = call
	}
}

func (r *Result) CompilerCall(id ast.NodeID) (CompilerCall, bool) {
	if r == nil || id == 0 {
		return CompilerCall{}, false
	}
	call, ok := r.calls.compilerCalls[id]
	return call, ok
}

func (r *Result) RecordCaseTest(id ast.NodeID, test CaseTest) {
	if r != nil && id != 0 {
		r.control.caseTests[id] = test
	}
}

func (r *Result) CaseTest(id ast.NodeID) (CaseTest, bool) {
	if r == nil || id == 0 {
		return CaseTest{}, false
	}
	test, ok := r.control.caseTests[id]
	return test, ok
}

func (r *Result) RecordMatch(id ast.NodeID, match Match) {
	if r != nil && id != 0 {
		r.control.matches[id] = match
	}
}

func (r *Result) Match(id ast.NodeID) (Match, bool) {
	if r == nil || id == 0 {
		return Match{}, false
	}
	match, ok := r.control.matches[id]
	return match, ok
}

// ArmBindings exposes the payload symbols one match arm binds, without leaking
// match artifacts into the effect producer. A discarded binding still binds
// storage, so it is reported like any other.
func (r *Result) ArmBindings(matchID ast.NodeID, caseIndex int) []*symbols.Symbol {
	evidence, found := r.Match(matchID)
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

func (r *Result) RecordForIteration(id ast.NodeID, iteration ForIteration) {
	if r != nil && id != 0 {
		r.control.forIterations[id] = iteration
	}
}

func (r *Result) ForgetForIteration(id ast.NodeID) {
	if r != nil {
		delete(r.control.forIterations, id)
	}
}

func (r *Result) ForIteration(id ast.NodeID) (ForIteration, bool) {
	if r == nil || id == 0 {
		return ForIteration{}, false
	}
	iteration, ok := r.control.forIterations[id]
	return iteration, ok
}

// SequenceCarrier exposes the hidden carrier a typed sequence loop keeps for
// the loop lifetime. Range loops have no carrier. Consumers ask this query
// instead of inspecting the concrete iteration plan themselves.
func (r *Result) SequenceCarrier(id ast.NodeID) (*symbols.Symbol, bool) {
	iteration, found := r.ForIteration(id)
	if !found {
		return nil, false
	}
	sequence, ok := iteration.Plan.(*SequenceIteration)
	if !ok || sequence == nil || sequence.Carrier == nil {
		return nil, false
	}
	return sequence.Carrier, true
}

func (r *Result) RecordCheckedIteration(id ast.NodeID, expansion *ast.BlockStmt) {
	if r != nil && id != 0 && expansion != nil {
		r.control.checkedIterations[id] = expansion
	}
}

func (r *Result) CheckedIteration(id ast.NodeID) *ast.BlockStmt {
	if r == nil || id == 0 {
		return nil
	}
	return r.control.checkedIterations[id]
}

func (r *Result) ForEachCheckedIteration(fn func(ast.NodeID, *ast.BlockStmt)) {
	if r == nil || fn == nil {
		return
	}
	for id, expansion := range r.control.checkedIterations {
		fn(id, expansion)
	}
}

// ForEachGeneratedNode visits every AST node synthesized and retained by
// typechecking. Consumers that build a node index should not know which
// evidence records contain generated roots.
func (r *Result) ForEachGeneratedNode(fn func(ast.Node)) {
	if r == nil || fn == nil {
		return
	}
	visit := func(node ast.Node) bool {
		if node != nil {
			fn(node)
		}
		return true
	}
	r.ForEachCheckedIteration(func(_ ast.NodeID, expansion *ast.BlockStmt) {
		ast.Inspect(expansion, visit)
	})
	r.ForEachCallArguments(func(_ ast.NodeID, args []ast.Expr) {
		for _, argument := range args {
			ast.Inspect(argument, visit)
		}
	})
}

// CloneReusableExpressionEvidenceFrom copies declaration-context facts that
// remain valid when syntax is cloned during default substitution. Contextual
// facts such as call-argument use, case subjects, and payload AST references
// are intentionally recomputed for the cloned expression.
func (r *Result) CloneReusableExpressionEvidenceFrom(dstID ast.NodeID, src *Result, srcID ast.NodeID) {
	if r == nil || src == nil || dstID == 0 || srcID == 0 {
		return
	}
	if typ := src.ExprType(srcID); typ != nil {
		r.RecordExprType(dstID, typ)
	}
	if src.ExpandedDefaultBinding(srcID) {
		r.MarkExpandedDefaultBinding(dstID)
	}
	if implementations := src.InterfaceImplementations(srcID); len(implementations) != 0 {
		r.RecordInterfaceImplementations(dstID, implementations)
	}
	if conversion, ok := src.ImplicitConversion(srcID); ok {
		r.RecordImplicitConversion(dstID, conversion)
	}
	if field, ok := src.StructField(srcID); ok {
		r.RecordStructField(dstID, field)
	}
}
