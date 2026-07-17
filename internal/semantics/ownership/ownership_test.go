package ownership

import (
	"slices"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/graph"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/place"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/table"
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

type ownershipResult struct {
	*diagnostics.DiagnosticBag
	ctx    *project.CompilerContext
	module *project.Module
}

func checkOwnershipSource(t *testing.T, src string) *ownershipResult {
	t.Helper()
	const filePath = "ownership_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "ownership_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	typechecker.Check(ctx, module)
	Check(ctx, module)
	return &ownershipResult{DiagnosticBag: diag, ctx: ctx, module: module}
}

func inspectFunctionAnalysis(t *testing.T, result *ownershipResult, name string) *analyzer {
	t.Helper()
	sym, found := result.module.ModuleScope.Lookup(name)
	if !found || sym == nil {
		t.Fatalf("function %q not found", name)
	}
	fn, ok := sym.ASTNode.(*ast.FnDecl)
	if !ok || fn == nil || fn.Body == nil {
		t.Fatalf("symbol %q does not have function body", name)
	}
	scope, ok := sym.Scope.(*table.Scope)
	if !ok || scope == nil {
		t.Fatalf("function %q scope missing", name)
	}
	analysis := &analyzer{
		ctx:           result.ctx,
		module:        result.module,
		flow:          build(result.module, fn.Body, scope),
		functionScope: scope,
		reportedJoin:  make(map[graph.NodeID]bool),
	}
	analysis.run()
	return analysis
}

func analysisNodeForStmt(t *testing.T, analysis *analyzer, stmt ast.Stmt) *flowNode {
	t.Helper()
	for _, node := range analysis.flow.nodes {
		if node != nil && node.kind == nodeStmt && node.stmt == stmt {
			return node
		}
	}
	t.Fatalf("flow node for %T not found", stmt)
	return nil
}

func hasOwnershipCode(result *ownershipResult, code string) bool {
	if result == nil {
		return false
	}
	for _, item := range result.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}

func cleanupSymbolNames(cleanup []*symbols.Symbol) []string {
	names := make([]string, 0, len(cleanup))
	for _, sym := range cleanup {
		if sym != nil {
			names = append(names, sym.Name)
		}
	}
	return names
}

func TestOwnedPointerBindingMovesImplicitly(t *testing.T) {
	diag := checkOwnershipSource(t, `fn make() -> *byte;

fn main() {
	let ptr: *byte = make();
	let duplicate = ptr;
	let invalid = ptr;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestRawPointerCopyAllowed(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let value: i32 = 1;
	let ptr: rawptr = @value;
	let duplicate = ptr;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestLiveOwnerFieldOverwritePlansDrop(t *testing.T) {
	result := checkOwnershipSource(t, `struct Holder { value: *i32 }
fn make() -> *i32;
fn bad(mut holder: Holder) {
	let next = make();
	holder.value = next;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected overwrite diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[2].(*ast.FnDecl)
	assign := fn.Body.Stmts[1].(*ast.AssignStmt)
	if _, ok := result.module.Semantics.DropBeforeAssign[assign.ID()]; !ok {
		t.Fatalf("missing drop-before-assignment plan")
	}
}

func TestLiveOwnerIndexedOverwritePlansDrop(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
fn replace(mut values: [1]*i32) {
	values[0] = make();
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected overwrite diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	assign := fn.Body.Stmts[0].(*ast.AssignStmt)
	if _, ok := result.module.Semantics.DropBeforeAssign[assign.ID()]; !ok {
		t.Fatalf("missing indexed drop-before-assignment plan")
	}
}

func TestFieldAssignmentRejectsRHSMoveOfTargetBase(t *testing.T) {
	result := checkOwnershipSource(t, `struct Box { ptr: *i32 }
fn replace(_: Box) -> *i32;
fn bad(mut box: Box) {
	box.ptr = replace(box);
}`)
	if !hasOwnershipCode(result, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected moved assignment-base diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestCleanupPlanUsesReverseLexicalOrder(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
fn main() {
	let first = make();
	{
		let nested = make();
	}
	let last = make();
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected cleanup diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	nested := fn.Body.Stmts[1].(*ast.BlockStmt)
	if got := cleanupSymbolNames(result.module.Semantics.CleanupAfterBlock[nested.ID()]); !slices.Equal(got, []string{"nested"}) {
		t.Fatalf("nested cleanup = %v, want [nested]", got)
	}
	if got := cleanupSymbolNames(result.module.Semantics.CleanupAfterBlock[fn.Body.ID()]); !slices.Equal(got, []string{"last", "first"}) {
		t.Fatalf("function cleanup = %v, want [last first]", got)
	}
}

func TestReturnCleanupSuppressesMovedResult(t *testing.T) {
	result := checkOwnershipSource(t, `fn pass(value: *i32, spare: *i32) -> *i32 {
	return value;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected return diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[0].(*ast.FnDecl)
	ret := fn.Body.Stmts[0].(*ast.ReturnStmt)
	if got := cleanupSymbolNames(result.module.Semantics.CleanupBeforeReturn[ret.ID()]); !slices.Equal(got, []string{"spare"}) {
		t.Fatalf("return cleanup = %v, want [spare]", got)
	}
}

func TestReturnCleanupClearsStateBeforeExitMerge(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
fn main(cond: bool) {
	let value = make();
	if cond {
		return;
	}
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected return diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	ret := fn.Body.Stmts[1].(*ast.IfStmt).Then.Stmts[0].(*ast.ReturnStmt)
	if got := cleanupSymbolNames(result.module.Semantics.CleanupBeforeReturn[ret.ID()]); !slices.Equal(got, []string{"value"}) {
		t.Fatalf("return cleanup = %v, want [value]", got)
	}
}

func TestReturnCleanupKeepsReplacementOwnerLive(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
fn main() -> i32 {
	let mut first = make();
	{
		let nested = make();
	}
	let next = make();
	first = next;
	let early = make();
	free(early);
	make();
	return 0;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected return diagnostics:\n%s", result.EmitAllToString())
	}
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	ret := fn.Body.Stmts[7].(*ast.ReturnStmt)
	if got := cleanupSymbolNames(result.module.Semantics.CleanupBeforeReturn[ret.ID()]); !slices.Equal(got, []string{"first"}) {
		t.Fatalf("return cleanup = %v, want [first]", got)
	}
}

func TestBranchOwnershipStateMustConverge(t *testing.T) {
	result := checkOwnershipSource(t, `fn consume(_: *i32) {}
fn bad(cond: bool, value: *i32) {
	if cond {
		consume(value);
	}
}`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidAssignment) ||
		!strings.Contains(result.EmitAllToString(), "ownership state differs across control-flow paths") {
		t.Fatalf("expected branch convergence diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestLoopOwnershipStateMustConverge(t *testing.T) {
	result := checkOwnershipSource(t, `fn consume(_: *i32) {}
fn bad(cond: bool, value: *i32) {
	for cond {
		consume(value);
	}
}`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidAssignment) ||
		!strings.Contains(result.EmitAllToString(), "ownership state differs across control-flow paths") {
		t.Fatalf("expected loop convergence diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestOwnershipTrackedModuleBindingRejected(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
const Global = make();`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidAssignment) ||
		!strings.Contains(result.EmitAllToString(), "ownership-tracked module bindings are not supported") {
		t.Fatalf("expected owned-global diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestMoveOnlyModuleBindingWithoutDropRejected(t *testing.T) {
	result := checkOwnershipSource(t, `struct Token { value: i32 }
const Global = .Token{ value = 1 };`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidAssignment) ||
		!strings.Contains(result.EmitAllToString(), "ownership-tracked module bindings are not supported") {
		t.Fatalf("expected move-only global diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestArrayLiteralConsumesCompositeElements(t *testing.T) {
	for _, literal := range []string{"[1]Point{point}", "[]Point{point}"} {
		diag := checkOwnershipSource(t, `struct Point { value: i32 }
fn consume(point: Point) {}
fn bad(point: Point) {
	let points = `+literal+`;
	consume(point);
}`)
		if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
			t.Fatalf("expected %s insertion to consume point, got:\n%s", literal, diag.EmitAllToString())
		}
	}
}

func TestUserCopyMethodWithValueReceiverConsumesCaller(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Point { value: i32 }
	fn (self: Point) copy() -> Point { return self; }
fn bad(point: Point) -> i32 {
	let duplicate = point.copy();
	return point.value + duplicate.value;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected copy method value receiver to consume caller, got:\n%s", diag.EmitAllToString())
	}
}

func TestConsumingOwnedInterfaceMethodMovesCarrier(t *testing.T) {
	diag := checkOwnershipSource(t, `iface Consumer { fn (Self) consume() }
struct Resource {}
fn (self: Resource) consume() {}
fn bad(resource: *Resource) {
	let consumer: *Consumer = resource;
	consumer.consume();
	free(consumer);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected consuming interface call to move carrier, got:\n%s", diag.EmitAllToString())
	}
}

func TestBorrowedCopyMethodCannotExtractOwnedField(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Owner { value: *i32 }
	fn (self: &Owner) copy() -> Owner { return .{ value = self.value }; }
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected borrowed copy method owner extraction rejection, got:\n%s", diag.EmitAllToString())
	}
}

func TestDiscardedOwnedProjectionFromTemporaryRejected(t *testing.T) {
	result := checkOwnershipSource(t, `struct Pair { first: *i32, second: *i32 }
fn make() -> Pair;
fn bad() {
	make().first;
}`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected temporary owner-projection diagnostic, got:\n%s", result.EmitAllToString())
	}
}

func TestMoveOnlyIndexedElementCopyRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn first(values: []*i32, index: usize) -> *i32 {
	return values[index];
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected indexed move-only copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "cannot be used by value") {
		t.Fatalf("expected permanent indexed move rejection, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveOnlySliceViewElementCopyRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn first(values: &[]*i32, index: usize) -> *i32 {
	return values[index];
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected indexed move-only copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
	if !strings.Contains(diag.EmitAllToString(), "cannot be used by value") {
		t.Fatalf("expected permanent indexed move rejection, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveOnlyIndexedElementCannotBeConsumedRepeatedly(t *testing.T) {
	diag := checkOwnershipSource(t, `fn consume(value: *i32) {}

fn duplicate(values: []*i32, index: usize) {
	consume(values[index]);
	consume(values[index]);
}`)
	out := diag.EmitAllToString()
	if count := strings.Count(out, "move-only indexed element"); count != 2 {
		t.Fatalf("expected two indexed move-only consume diagnostics, got %d:\n%s", count, out)
	}
}

func TestCopyableIndexedElementReadAllowed(t *testing.T) {
	diag := checkOwnershipSource(t, `fn first(values: []i32, index: usize) -> i32 {
	return values[index];
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReferenceReceiverDoesNotCopyMoveOnlyOwner(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Counter {
	value: i32
}

	fn (self: &Counter) get() -> i32 {
		return self.value;
	}

fn main() -> i32 {
	let c: Counter = .{ value = 1 };
	c.get();
	return c.get();
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableReferenceArgumentIsReborrowed(t *testing.T) {
	diag := checkOwnershipSource(t, `fn sink(value: &mut i32) {}

fn forward(value: &mut i32) {
	sink(value);
	sink(value);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestMutableReferenceBindingTransfersImplicitly(t *testing.T) {
	diag := checkOwnershipSource(t, `fn duplicate(reference: &mut i32) {
	let alias = reference;
	let invalid = reference;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMutableReferenceBindingCanMove(t *testing.T) {
	diag := checkOwnershipSource(t, `fn transfer(reference: &mut i32) {
	let alias = reference;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReferenceOriginsNormalizeLocalReborrowProjection(t *testing.T) {
	result := checkOwnershipSource(t, `struct Holder { values: [2]i32 }
fn inspect(mut holder: Holder) {
	let base = &mut holder;
	let first = &mut base.values[1];
	let second = first;
	let final = second;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}
	analysis := inspectFunctionAnalysis(t, result, "inspect")
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	finalNode := analysisNodeForStmt(t, analysis, fn.Body.Stmts[3])
	holder, _ := analysis.functionScope.Lookup("holder")
	second, _ := analysis.functionScope.Lookup("second")
	want := []place.Origin{{Root: holder, Projections: []place.OriginProjection{
		{Kind: place.OriginField, Field: "values"},
		{Kind: place.OriginIndex, Index: "1"},
	}}}
	if got := analysis.inStates[finalNode.id].references[second].origins; !place.SameOrigins(got, want) {
		t.Fatalf("second origins = %#v, want %#v", got, want)
	}
}

func TestReferenceOriginsCanonicalizeEquivalentConstantIndexes(t *testing.T) {
	result := checkOwnershipSource(t, `fn inspect(values: [2]i32) {
	let decimal = &values[1];
	let padded = &values[01];
	let hexadecimal = &values[0x1];
	let final = decimal;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}
	analysis := inspectFunctionAnalysis(t, result, "inspect")
	fn := result.module.AST.Stmts[0].(*ast.FnDecl)
	finalNode := analysisNodeForStmt(t, analysis, fn.Body.Stmts[3])
	values, _ := analysis.functionScope.Lookup("values")
	want := []place.Origin{{Root: values, Projections: []place.OriginProjection{{Kind: place.OriginIndex, Index: "1"}}}}
	for _, name := range []string{"decimal", "padded", "hexadecimal"} {
		sym, _ := analysis.functionScope.Lookup(name)
		if got := analysis.inStates[finalNode.id].references[sym].origins; !place.SameOrigins(got, want) {
			t.Fatalf("%s origins = %#v, want %#v", name, got, want)
		}
	}
}

func TestOptionalReferenceOriginsAndLiveness(t *testing.T) {
	result := checkOwnershipSource(t, `fn local(value: i32) {
	let maybe: ?&i32 = &value;
	let copied = maybe;
	if copied == none {}
}

fn mutable(mut value: i32) {
	let maybe: ?&mut i32 = &mut value;
	if maybe == none {}
}

fn clear(value: i32) {
	let mut maybe: ?&i32 = &value;
	maybe = none;
	let marker = 0;
}

fn parameter(maybe: ?&i32) {
	if maybe == none {}
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}

	local := inspectFunctionAnalysis(t, result, "local")
	localFn := result.module.AST.Stmts[0].(*ast.FnDecl)
	localUse := analysisNodeForStmt(t, local, localFn.Body.Stmts[2])
	value, _ := local.functionScope.Lookup("value")
	copied, _ := local.functionScope.Lookup("copied")
	want := []place.Origin{{Root: value}}
	if got := local.inStates[localUse.id].references[copied].origins; !place.SameOrigins(got, want) {
		t.Fatalf("copied optional origins = %#v, want %#v", got, want)
	}
	if _, live := local.referenceLiveIn[localUse.id][copied]; !live {
		t.Fatalf("optional reference not live at none comparison")
	}

	mutable := inspectFunctionAnalysis(t, result, "mutable")
	mutableFn := result.module.AST.Stmts[1].(*ast.FnDecl)
	mutableUse := analysisNodeForStmt(t, mutable, mutableFn.Body.Stmts[1])
	maybeMutable, _ := mutable.functionScope.Lookup("maybe")
	if tracked := mutable.inStates[mutableUse.id].references[maybeMutable]; !tracked.mutable {
		t.Fatalf("optional mutable reference lost mutable loan kind")
	}

	clear := inspectFunctionAnalysis(t, result, "clear")
	clearFn := result.module.AST.Stmts[2].(*ast.FnDecl)
	marker := analysisNodeForStmt(t, clear, clearFn.Body.Stmts[2])
	maybeCleared, _ := clear.functionScope.Lookup("maybe")
	if _, tracked := clear.inStates[marker.id].references[maybeCleared]; tracked {
		t.Fatalf("none assignment retained optional reference origins")
	}

	parameter := inspectFunctionAnalysis(t, result, "parameter")
	parameterFn := result.module.AST.Stmts[3].(*ast.FnDecl)
	parameterUse := analysisNodeForStmt(t, parameter, parameterFn.Body.Stmts[0])
	maybeParameter, _ := parameter.functionScope.Lookup("maybe")
	parameterValue := parameter.inStates[parameterUse.id].references[maybeParameter]
	if !place.SameOrigins(parameterValue.origins, []place.Origin{{Root: maybeParameter}}) {
		t.Fatalf("optional reference parameter origins = %#v", parameterValue.origins)
	}
	if _, live := parameter.referenceLiveIn[parameterUse.id][maybeParameter]; !live {
		t.Fatalf("optional reference parameter not live at none comparison")
	}
}

func TestReferenceOriginsUnionAtConditionalJoin(t *testing.T) {
	result := checkOwnershipSource(t, `fn choose(cond: bool, left: i32, right: i32) {
	let mut selected = &left;
	if cond {
		selected = &right;
	}
	let copied = selected;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}
	analysis := inspectFunctionAnalysis(t, result, "choose")
	fn := result.module.AST.Stmts[0].(*ast.FnDecl)
	copyNode := analysisNodeForStmt(t, analysis, fn.Body.Stmts[2])
	selected, _ := analysis.functionScope.Lookup("selected")
	left, _ := analysis.functionScope.Lookup("left")
	right, _ := analysis.functionScope.Lookup("right")
	want := []place.Origin{{Root: left}, {Root: right}}
	if got := analysis.inStates[copyNode.id].references[selected].origins; !place.SameOrigins(got, want) {
		t.Fatalf("joined origins = %#v, want %#v", got, want)
	}
}

func TestReferenceLivenessEndsAfterLastUse(t *testing.T) {
	result := checkOwnershipSource(t, `fn Read(_: &i32) {}
fn inspect(mut value: i32) {
	let reference = &value;
	Read(reference);
	value = 2;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}
	analysis := inspectFunctionAnalysis(t, result, "inspect")
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	callNode := analysisNodeForStmt(t, analysis, fn.Body.Stmts[1])
	assignNode := analysisNodeForStmt(t, analysis, fn.Body.Stmts[2])
	reference, _ := analysis.functionScope.Lookup("reference")
	if _, live := analysis.referenceLiveIn[callNode.id][reference]; !live {
		t.Fatalf("reference not live at its final use")
	}
	if _, live := analysis.referenceLiveOut[callNode.id][reference]; live {
		t.Fatalf("reference remains live after final use")
	}
	if _, live := analysis.referenceLiveIn[assignNode.id][reference]; live {
		t.Fatalf("reference remains live at following assignment")
	}
}

func TestReferenceLivenessEndsAfterConditionalUse(t *testing.T) {
	result := checkOwnershipSource(t, `fn inspect(value: i32) {
	let maybe: ?&i32 = &value;
	if maybe == none {
		let thenMarker = 0;
	} else {
		let elseMarker = 0;
	}
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}
	analysis := inspectFunctionAnalysis(t, result, "inspect")
	fn := result.module.AST.Stmts[0].(*ast.FnDecl)
	conditional := fn.Body.Stmts[1].(*ast.IfStmt)
	conditionNode := analysisNodeForStmt(t, analysis, conditional)
	thenNode := analysisNodeForStmt(t, analysis, conditional.Then.Stmts[0])
	elseBlock := conditional.Else.(*ast.BlockStmt)
	elseNode := analysisNodeForStmt(t, analysis, elseBlock.Stmts[0])
	maybe, _ := analysis.functionScope.Lookup("maybe")
	if _, live := analysis.referenceLiveIn[conditionNode.id][maybe]; !live {
		t.Fatalf("reference not live at conditional use")
	}
	if _, live := analysis.referenceLiveOut[conditionNode.id][maybe]; live {
		t.Fatalf("reference remains live after conditional use")
	}
	if _, live := analysis.referenceLiveIn[thenNode.id][maybe]; live {
		t.Fatalf("reference remains live in then branch")
	}
	if _, live := analysis.referenceLiveIn[elseNode.id][maybe]; live {
		t.Fatalf("reference remains live in else branch")
	}
}

func TestReferenceLivenessIncludesLoopBackedge(t *testing.T) {
	result := checkOwnershipSource(t, `fn Read(_: &i32) {}
fn inspect(cond: bool, value: i32) {
	let reference = &value;
	for cond {
		Read(reference);
	}
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}
	analysis := inspectFunctionAnalysis(t, result, "inspect")
	fn := result.module.AST.Stmts[1].(*ast.FnDecl)
	loopNode := analysisNodeForStmt(t, analysis, fn.Body.Stmts[1])
	reference, _ := analysis.functionScope.Lookup("reference")
	if _, live := analysis.referenceLiveIn[loopNode.id][reference]; !live {
		t.Fatalf("loop body reference use did not propagate through header")
	}
}

func TestReferenceLivenessIgnoresLoopExitJoin(t *testing.T) {
	result := checkOwnershipSource(t, `fn inspect(value: i32) {
	let maybe: ?&i32 = &value;
	for maybe == none {}
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}
	analysis := inspectFunctionAnalysis(t, result, "inspect")
	fn := result.module.AST.Stmts[0].(*ast.FnDecl)
	loop := fn.Body.Stmts[1].(*ast.ForStmt)
	var join *flowNode
	for _, node := range analysis.flow.nodes {
		if node != nil && node.kind == nodeJoin && node.stmt == loop {
			join = node
			break
		}
	}
	if join == nil {
		t.Fatalf("loop exit join not found")
	}
	maybe, _ := analysis.functionScope.Lookup("maybe")
	if _, live := analysis.referenceLiveIn[join.id][maybe]; live {
		t.Fatalf("synthetic loop exit repeats condition use")
	}
}

func TestImplicitBindingTransfersNoCopyValue(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	let next = current;
	destroy(next);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestNoCopyBindingMovesImplicitly(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	let next = current;
	destroy(current);
	destroy(next);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestUseAfterMoveRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	let next = current;
	destroy(current);
	destroy(next);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestValueParamConsumesArgument(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	destroy(current);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPlainValueParamConsumesArgument(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn inspect(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	inspect(current);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReassignmentClearsMovedLocal(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	value: i32,
}

fn destroy(data: Buffer) {}

fn main() {
	let mut current: Buffer = .{ value = 1 };
	destroy(current);
	current = .{ value = 2 };
	destroy(current);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayOwnerOperationsConsumeAndReinitializeOwner(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let mut values = []i32{};
	values = append(values, 1);
	values = reserve(values, 8);
	values = resize(values, 4, 0);
	values = shrink(values, 2);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayShrinkConsumesOwner(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let values = []i32{1};
	let shortened = shrink(values, 0);
	print(values[0]);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected moved-owner diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayAppendConsumesOwner(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let values = []i32{};
	let extended = append(values, 1);
	print(values[0]);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected moved-owner diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayAppendConsumesCompositeElement(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Point { x: i32 }
fn consume(point: Point) {}
fn main() {
	let point = .Point{x = 1};
	let values = append([]Point{}, point);
	consume(point);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected moved-element diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestNoCopyArgumentToPlainParamMoves(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn inspect(data: Buffer) {}

fn main() {
	let current: Buffer = get_buffer();
	inspect(current);
	inspect(current);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestFreeConsumesOwnedPointer(t *testing.T) {
	diag := checkOwnershipSource(t, `fn get_value() -> *i32;
fn main() {
	let value = get_value();
	free(value);
	free(value);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-free diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestValueReceiverConsumesBinding(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;

	fn (self: Buffer) close() {}

fn main() {
	let current: Buffer = get_buffer();
	current.close();
	current.close();
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestNoCopyFieldSubexpressionRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

struct Holder {
	buf: Buffer,
}

fn get_buffer() -> Buffer;

fn main() {
	let holder: Holder = .{ buf = get_buffer() };
	let next = holder.buf;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrInvalidCopy) {
		t.Fatalf("expected invalid copy diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveInBranchRejectsUseAfterJoin(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main(flag: bool) {
	let current: Buffer = get_buffer();
	if flag {
		destroy(current);
	}
	destroy(current);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestMoveInLoopRejectsLaterUse(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Buffer {
	ptr: *u8,
}

fn get_buffer() -> Buffer;
fn destroy(data: Buffer) {}

fn main(flag: bool) {
	let current: Buffer = get_buffer();
	for flag {
		destroy(current);
	}
	destroy(current);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnAddressOfLocalRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn bad() -> rawptr {
	let value: i32 = 1;
	return @value;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrPointerEscape) {
		t.Fatalf("expected pointer escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnAddressOfLocalIndexedElementRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn bad() -> rawptr {
	let values = [_]i32{1};
	return @values[0];
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrPointerEscape) {
		t.Fatalf("expected indexed pointer escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnLocalPointerBindingRejected(t *testing.T) {
	diag := checkOwnershipSource(t, `fn bad() -> rawptr {
	let value: i32 = 1;
	let ptr: rawptr = @value;
	return ptr;
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrPointerEscape) {
		t.Fatalf("expected pointer escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnAddressOfModuleGlobalAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `const global: i32 = 1;

fn get() -> rawptr {
	return @global;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReturnModuleGlobalPointerBindingAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `const global: i32 = 1;

fn get() -> rawptr {
	let ptr: rawptr = @global;
	return ptr;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReturnPointerParamAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `fn identity(ptr: rawptr) -> rawptr {
	return ptr;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestReturnExternPointerAccepted(t *testing.T) {
	diag := checkOwnershipSource(t, `#[extern]
fn open_value() -> rawptr;

fn get() -> rawptr {
	return open_value();
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}
