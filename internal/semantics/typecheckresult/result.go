// Package typecheckresult defines semantic evidence produced by base typechecking.
package typecheckresult

import (
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/intrinsics"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/source"
)

// InterfaceImplementation proves that one declared method materializes an interface slot.
type InterfaceImplementation struct {
	Symbol       *symbols.Symbol
	CallableType *typeinfo.FuncType
}

type CaseTest struct {
	SubjectID       source.NodeID
	Case            int
	MatchesWhenTrue bool
	CaseCount       int
	Family          typeinfo.VariantFamily
}

// Match records typechecker-owned case and binding evidence consumed by CFG
// and later semantic phases without resolving source paths again.
type Match struct {
	SubjectID source.NodeID
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
	ArmID    source.NodeID
	BodyID   source.NodeID
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
	IsDiscard  bool
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
	HasGuaranteedEntry bool

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
	types                    map[source.NodeID]typeinfo.Type
	expandedDefaultBindings  map[source.NodeID]struct{}
	interfaceImplementations map[source.NodeID][]InterfaceImplementation
	implicitConversions      map[source.NodeID]typeinfo.Conversion
	stringConcatenations     map[source.NodeID]struct{}
	structFields             map[source.NodeID]StructFieldAccess
	constantIndexes          map[source.NodeID]ConstantIndex
	structLiteralFields      map[source.NodeID][]ast.Expr
	variantConstructions     map[source.NodeID]VariantConstruction
	valueUses                map[source.NodeID]typeinfo.UseKind
	referenceArguments       map[source.NodeID]bool
}

type callEvidence struct {
	effectiveArguments map[source.NodeID][]ast.Expr
	implicitArguments  map[source.NodeID]typeinfo.Type
	compilerCalls      map[source.NodeID]CompilerCall
}

type controlEvidence struct {
	caseTests         map[source.NodeID]CaseTest
	matches           map[source.NodeID]Match
	forIterations     map[source.NodeID]ForIteration
	checkedIterations map[source.NodeID]*ast.BlockStmt
}

func New() *Result {
	return &Result{
		expressions: expressionEvidence{
			types:                    make(map[source.NodeID]typeinfo.Type),
			expandedDefaultBindings:  make(map[source.NodeID]struct{}),
			interfaceImplementations: make(map[source.NodeID][]InterfaceImplementation),
			implicitConversions:      make(map[source.NodeID]typeinfo.Conversion),
			stringConcatenations:     make(map[source.NodeID]struct{}),
			structFields:             make(map[source.NodeID]StructFieldAccess),
			constantIndexes:          make(map[source.NodeID]ConstantIndex),
			structLiteralFields:      make(map[source.NodeID][]ast.Expr),
			variantConstructions:     make(map[source.NodeID]VariantConstruction),
			valueUses:                make(map[source.NodeID]typeinfo.UseKind),
			referenceArguments:       make(map[source.NodeID]bool),
		},
		calls: callEvidence{
			effectiveArguments: make(map[source.NodeID][]ast.Expr),
			implicitArguments:  make(map[source.NodeID]typeinfo.Type),
			compilerCalls:      make(map[source.NodeID]CompilerCall),
		},
		control: controlEvidence{
			caseTests:         make(map[source.NodeID]CaseTest),
			matches:           make(map[source.NodeID]Match),
			forIterations:     make(map[source.NodeID]ForIteration),
			checkedIterations: make(map[source.NodeID]*ast.BlockStmt),
		},
	}
}

func (r *Result) RecordExprType(id source.NodeID, typ typeinfo.Type) {
	if r == nil || !id.IsValid() || typ == nil {
		return
	}
	r.expressions.types[id] = typ
}

func (r *Result) ExprType(id source.NodeID) typeinfo.Type {
	if r == nil || !id.IsValid() {
		return nil
	}
	return r.expressions.types[id]
}

func (r *Result) MarkExpandedDefaultBinding(id source.NodeID) {
	if r != nil && id.IsValid() {
		r.expressions.expandedDefaultBindings[id] = struct{}{}
	}
}

func (r *Result) ExpandedDefaultBinding(id source.NodeID) bool {
	if r == nil || !id.IsValid() {
		return false
	}
	_, ok := r.expressions.expandedDefaultBindings[id]
	return ok
}

func (r *Result) RecordInterfaceImplementations(id source.NodeID, implementations []InterfaceImplementation) {
	if r == nil || !id.IsValid() || len(implementations) == 0 {
		return
	}
	r.expressions.interfaceImplementations[id] = implementations
}

func (r *Result) InterfaceImplementations(id source.NodeID) []InterfaceImplementation {
	if r == nil || !id.IsValid() {
		return nil
	}
	return r.expressions.interfaceImplementations[id]
}

func (r *Result) RecordImplicitConversion(id source.NodeID, conversion typeinfo.Conversion) {
	if r != nil && id.IsValid() {
		r.expressions.implicitConversions[id] = conversion
	}
}

func (r *Result) ImplicitConversion(id source.NodeID) (typeinfo.Conversion, bool) {
	if r == nil || !id.IsValid() {
		return typeinfo.Conversion{}, false
	}
	conversion, ok := r.expressions.implicitConversions[id]
	return conversion, ok
}

func (r *Result) MarkStringConcatenation(id source.NodeID) {
	if r != nil && id.IsValid() {
		r.expressions.stringConcatenations[id] = struct{}{}
	}
}

// StringConcatenation reports whether a binary expression was resolved as a
// string concatenation, which consumes its left operand.
func (r *Result) StringConcatenation(id source.NodeID) bool {
	if r == nil || !id.IsValid() {
		return false
	}
	_, found := r.expressions.stringConcatenations[id]
	return found
}

func (r *Result) RecordStructField(id source.NodeID, access StructFieldAccess) {
	if r != nil && id.IsValid() {
		r.expressions.structFields[id] = access
	}
}

func (r *Result) StructField(id source.NodeID) (StructFieldAccess, bool) {
	if r == nil || !id.IsValid() {
		return StructFieldAccess{}, false
	}
	access, ok := r.expressions.structFields[id]
	return access, ok
}

func (r *Result) RecordConstantIndex(id source.NodeID, value ConstantIndex) {
	if r == nil || !id.IsValid() || value.Text == "" || value.Type == nil {
		return
	}
	r.expressions.constantIndexes[id] = value
}

func (r *Result) ConstantIndex(id source.NodeID) (ConstantIndex, bool) {
	if r == nil || !id.IsValid() {
		return ConstantIndex{}, false
	}
	value, ok := r.expressions.constantIndexes[id]
	return value, ok
}

func (r *Result) RecordStructLiteralFields(id source.NodeID, fields []ast.Expr) {
	if r == nil || !id.IsValid() || fields == nil {
		return
	}
	r.expressions.structLiteralFields[id] = append([]ast.Expr(nil), fields...)
}

func (r *Result) StructLiteralFields(id source.NodeID) ([]ast.Expr, bool) {
	if r == nil || !id.IsValid() {
		return nil, false
	}
	fields, ok := r.expressions.structLiteralFields[id]
	return append([]ast.Expr(nil), fields...), ok
}

func (r *Result) RecordVariantConstruction(id source.NodeID, construction VariantConstruction) {
	if r != nil && id.IsValid() {
		r.expressions.variantConstructions[id] = construction
	}
}

func (r *Result) VariantConstruction(id source.NodeID) (VariantConstruction, bool) {
	if r == nil || !id.IsValid() {
		return VariantConstruction{}, false
	}
	construction, ok := r.expressions.variantConstructions[id]
	return construction, ok
}

func (r *Result) RecordValueUse(id source.NodeID, use typeinfo.UseKind) {
	if r != nil && id.IsValid() {
		r.expressions.valueUses[id] = use
	}
}

// ValueUse exposes the use kind the typechecker decided for one expression.
// Coverage is call arguments today, so an absent answer is normal rather than
// a missing decision.
func (r *Result) ValueUse(id source.NodeID) (typeinfo.UseKind, bool) {
	if r == nil || !id.IsValid() {
		return typeinfo.UseRead, false
	}
	kind, found := r.expressions.valueUses[id]
	return kind, found
}

func (r *Result) ForEachValueUse(fn func(source.NodeID, typeinfo.UseKind)) {
	if r == nil || fn == nil {
		return
	}
	for id, use := range r.expressions.valueUses {
		fn(id, use)
	}
}

func (r *Result) RecordReferenceArgument(id source.NodeID, isMutable bool) {
	if r != nil && id.IsValid() {
		r.expressions.referenceArguments[id] = isMutable
	}
}

// ReferenceArgument reports whether an argument's parameter is a reference and,
// when it is, whether that reference is mutable.
func (r *Result) ReferenceArgument(id source.NodeID) (isMutable bool, found bool) {
	if r == nil || !id.IsValid() {
		return false, false
	}
	isMutable, found = r.expressions.referenceArguments[id]
	return isMutable, found
}

func (r *Result) RecordCallArguments(id source.NodeID, args []ast.Expr) {
	if r == nil || !id.IsValid() {
		return
	}
	r.calls.effectiveArguments[id] = args
}

func (r *Result) CallArguments(id source.NodeID) ([]ast.Expr, bool) {
	if r == nil || !id.IsValid() {
		return nil, false
	}
	args, ok := r.calls.effectiveArguments[id]
	return args, ok
}

func (r *Result) ForEachCallArguments(fn func(source.NodeID, []ast.Expr)) {
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

func (r *Result) RecordImplicitCallArgument(id source.NodeID, typ typeinfo.Type) {
	if r != nil && id.IsValid() && typ != nil {
		r.calls.implicitArguments[id] = typ
	}
}

func (r *Result) ImplicitCallArgument(id source.NodeID) typeinfo.Type {
	if r == nil || !id.IsValid() {
		return nil
	}
	return r.calls.implicitArguments[id]
}

func (r *Result) RecordCompilerCall(id source.NodeID, call CompilerCall) {
	if r != nil && id.IsValid() {
		r.calls.compilerCalls[id] = call
	}
}

func (r *Result) CompilerCall(id source.NodeID) (CompilerCall, bool) {
	if r == nil || !id.IsValid() {
		return CompilerCall{}, false
	}
	call, ok := r.calls.compilerCalls[id]
	return call, ok
}

func (r *Result) RecordCaseTest(id source.NodeID, test CaseTest) {
	if r != nil && id.IsValid() {
		r.control.caseTests[id] = test
	}
}

func (r *Result) CaseTest(id source.NodeID) (CaseTest, bool) {
	if r == nil || !id.IsValid() {
		return CaseTest{}, false
	}
	test, ok := r.control.caseTests[id]
	return test, ok
}

func (r *Result) RecordMatch(id source.NodeID, match Match) {
	if r != nil && id.IsValid() {
		r.control.matches[id] = match
	}
}

func (r *Result) Match(id source.NodeID) (Match, bool) {
	if r == nil || !id.IsValid() {
		return Match{}, false
	}
	match, ok := r.control.matches[id]
	return match, ok
}

// ArmBindings exposes the payload symbols one match arm binds, without leaking
// match artifacts into the effect producer. A discarded binding still binds
// storage, so it is reported like any other.
func (r *Result) ArmBindings(matchID source.NodeID, caseIndex int) []*symbols.Symbol {
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

func (r *Result) RecordForIteration(id source.NodeID, iteration ForIteration) {
	if r != nil && id.IsValid() {
		r.control.forIterations[id] = iteration
	}
}

func (r *Result) ForgetForIteration(id source.NodeID) {
	if r != nil {
		delete(r.control.forIterations, id)
	}
}

func (r *Result) ForIteration(id source.NodeID) (ForIteration, bool) {
	if r == nil || !id.IsValid() {
		return ForIteration{}, false
	}
	iteration, ok := r.control.forIterations[id]
	return iteration, ok
}

// SequenceCarrier exposes the hidden carrier a typed sequence loop keeps for
// the loop lifetime. Range loops have no carrier. Consumers ask this query
// instead of inspecting the concrete iteration plan themselves.
func (r *Result) SequenceCarrier(id source.NodeID) (*symbols.Symbol, bool) {
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

func (r *Result) RecordCheckedIteration(id source.NodeID, expansion *ast.BlockStmt) {
	if r != nil && id.IsValid() && expansion != nil {
		r.control.checkedIterations[id] = expansion
	}
}

func (r *Result) CheckedIteration(id source.NodeID) *ast.BlockStmt {
	if r == nil || !id.IsValid() {
		return nil
	}
	return r.control.checkedIterations[id]
}

func (r *Result) ForEachCheckedIteration(fn func(source.NodeID, *ast.BlockStmt)) {
	if r == nil || fn == nil {
		return
	}
	for id, expansion := range r.control.checkedIterations {
		fn(id, expansion)
	}
}

// CloneReusableExpressionEvidenceFrom copies declaration-context facts that
// remain valid when syntax is cloned during default substitution. Contextual
// facts such as call-argument use, case subjects, and payload AST references
// are intentionally recomputed for the cloned expression.
func (r *Result) CloneReusableExpressionEvidenceFrom(dstID source.NodeID, src *Result, srcID source.NodeID) {
	if r == nil || src == nil || !dstID.IsValid() || !srcID.IsValid() {
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
