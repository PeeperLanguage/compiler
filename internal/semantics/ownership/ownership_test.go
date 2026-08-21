package ownership

import (
	"slices"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir"
	"compiler/internal/ir/hir/lower"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/ownershipresult"
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
	module.HIR = lower.GenerateHIR(ctx, module)
	module.CFG = cfg.BuildModule(module.HIR)
	module.Ownership = Check(ctx, module)
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
	cfgFn := cfgForFunction(result.module, fn)
	if cfgFn == nil {
		t.Fatalf("function %q cleanup plan missing", name)
	}
	cleanup := cleanupPlanForFunction(t, result, fn)
	sites, order := indexSites(result.module, cfgFn, fn.Body, scope)
	analysis := &analyzer{
		ctx:           result.ctx,
		module:        result.module,
		graph:         cfgFn,
		sites:         sites,
		order:         order,
		cleanup:       cleanup,
		function:      fn,
		functionScope: scope,
		reportedJoin:  make(map[cfg.SiteID]bool),
	}
	analysis.run()
	return analysis
}

func analysisNodeForStmt(t *testing.T, analysis *analyzer, stmt ast.Stmt) *site {
	t.Helper()
	for _, node := range analysis.sites {
		if node != nil && node.flow != nil &&
			(node.flow.Kind == cfg.SiteStatement || node.flow.Kind == cfg.SiteTerminator) && node.stmt == stmt {
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

func cleanupPlanForFunction(t *testing.T, result *ownershipResult, fn *ast.FnDecl) *ownershipresult.CleanupPlan {
	t.Helper()
	plan := result.module.Ownership[ir.NodeID(fn.ID())]
	if plan == nil {
		t.Fatalf("cleanup plan for %q missing", fn.Name.Name)
	}
	return plan
}

func cleanupSymbolNames(module *project.Module, cleanup []symbols.SymbolID) []string {
	names := make(map[symbols.SymbolID]string)
	if module != nil && module.ModuleScope != nil {
		for _, sym := range module.ModuleScope.Symbols() {
			if sym != nil {
				names[sym.ID] = sym.Name
			}
		}
	}
	if module != nil && module.Semantics != nil {
		for _, scope := range module.Semantics.BlockScopes {
			for _, sym := range scope.Symbols() {
				if sym != nil {
					names[sym.ID] = sym.Name
				}
			}
		}
	}
	out := make([]string, 0, len(cleanup))
	for _, id := range cleanup {
		out = append(out, names[id])
	}
	return out
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

func TestAllocConsumesOwnedValue(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let q = alloc(42);
	let p = alloc(q);
	free(p);
	free(q);
}`)
	if !hasOwnershipCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected alloc to consume owned argument, got:\n%s", diag.EmitAllToString())
	}
}

func TestInvalidAllocAritiesDoNotPanic(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{name: "missing value", src: `fn main() { alloc(); }`, want: "wrong number of arguments: got 0, want 1"},
		{name: "direct excess", src: `fn main() { alloc(1, 2, 3); }`, want: "wrong number of arguments: got 3, want 2"},
		{name: "piped excess", src: `fn main() { 1 |> alloc(2, 3); }`, want: "wrong number of arguments: got 2, want 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkOwnershipSource(t, tt.src)
			if out := result.EmitAllToString(); !strings.Contains(out, tt.want) {
				t.Fatalf("unexpected alloc arity diagnostic:\n%s", out)
			}
		})
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

func TestOwnershipCheckClearsAllDerivedPlans(t *testing.T) {
	result := checkOwnershipSource(t, `fn main() {
	let value: i32 = 1;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}

	fn := result.module.AST.Stmts[0].(*ast.FnDecl)
	plan := cleanupPlanForFunction(t, result, fn)
	staleID := ir.NodeID(999999)
	plan.AfterScope[staleID] = []symbols.SymbolID{999999}
	plan.BeforeReturn[staleID] = []symbols.SymbolID{999999}
	plan.BeforeAssign[staleID] = struct{}{}
	plan.DiscardedValue[staleID] = struct{}{}
	plan.ProjectionBase[staleID] = struct{}{}

	result.module.Ownership = Check(result.ctx, result.module)
	plan = cleanupPlanForFunction(t, result, fn)
	if len(plan.AfterScope) != 0 || len(plan.BeforeReturn) != 0 || len(plan.BeforeAssign) != 0 ||
		len(plan.DiscardedValue) != 0 || len(plan.ProjectionBase) != 0 {
		t.Fatalf("stale ownership plans survived rerun: %#v", plan)
	}
}

func TestOwnershipPlansStayWithOwningCFGFunction(t *testing.T) {
	result := checkOwnershipSource(t, `fn make() -> *i32;
fn first() { let one = make(); }
fn second() { let two = make(); }`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}
	first := result.module.AST.Stmts[1].(*ast.FnDecl)
	second := result.module.AST.Stmts[2].(*ast.FnDecl)
	firstPlan := cleanupPlanForFunction(t, result, first)
	secondPlan := cleanupPlanForFunction(t, result, second)
	if len(firstPlan.AfterScope) != 1 || len(secondPlan.AfterScope) != 1 {
		t.Fatalf("unexpected function cleanup plans: first=%#v second=%#v", firstPlan, secondPlan)
	}
	if _, found := firstPlan.AfterScope[ir.NodeID(second.Body.ID())]; found {
		t.Fatalf("first function received second function cleanup")
	}
	if _, found := secondPlan.AfterScope[ir.NodeID(first.Body.ID())]; found {
		t.Fatalf("second function received first function cleanup")
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
	if _, ok := cleanupPlanForFunction(t, result, fn).BeforeAssign[ir.NodeID(assign.ID())]; !ok {
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
	if _, ok := cleanupPlanForFunction(t, result, fn).BeforeAssign[ir.NodeID(assign.ID())]; !ok {
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
	plan := cleanupPlanForFunction(t, result, fn)
	if got := cleanupSymbolNames(result.module, plan.AfterScope[ir.NodeID(nested.ID())]); !slices.Equal(got, []string{"nested"}) {
		t.Fatalf("nested cleanup = %v, want [nested]", got)
	}
	if got := cleanupSymbolNames(result.module, plan.AfterScope[ir.NodeID(fn.Body.ID())]); !slices.Equal(got, []string{"last", "first"}) {
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
	if got := cleanupSymbolNames(result.module, cleanupPlanForFunction(t, result, fn).BeforeReturn[ir.NodeID(ret.ID())]); !slices.Equal(got, []string{"spare"}) {
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
	if got := cleanupSymbolNames(result.module, cleanupPlanForFunction(t, result, fn).BeforeReturn[ir.NodeID(ret.ID())]); !slices.Equal(got, []string{"value"}) {
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
	if got := cleanupSymbolNames(result.module, cleanupPlanForFunction(t, result, fn).BeforeReturn[ir.NodeID(ret.ID())]); !slices.Equal(got, []string{"first"}) {
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

func TestOwnershipHandlesByteAndCharLiterals(t *testing.T) {
	result := checkOwnershipSource(t, `fn main() -> i32 {
	let byte: byte = b'a';
	let char: char = 'é';
	return byte as i32;
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
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
	diag := checkOwnershipSource(t, `fn first(values: &[..]*i32, index: usize) -> *i32 {
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
	if got := referenceOrigins(analysis.inStates[finalNode.flow.ID].references[second]); !place.SameOrigins(got, want) {
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
		if got := referenceOrigins(analysis.inStates[finalNode.flow.ID].references[sym]); !place.SameOrigins(got, want) {
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
	if got := referenceOrigins(local.inStates[localUse.flow.ID].references[copied]); !place.SameOrigins(got, want) {
		t.Fatalf("copied optional origins = %#v, want %#v", got, want)
	}
	if _, live := local.referenceLiveIn[localUse.flow.ID][copied]; !live {
		t.Fatalf("optional reference not live at none comparison")
	}

	mutable := inspectFunctionAnalysis(t, result, "mutable")
	mutableFn := result.module.AST.Stmts[1].(*ast.FnDecl)
	mutableUse := analysisNodeForStmt(t, mutable, mutableFn.Body.Stmts[1])
	maybeMutable, _ := mutable.functionScope.Lookup("maybe")
	if tracked := mutable.inStates[mutableUse.flow.ID].references[maybeMutable]; len(tracked) != 1 || !tracked[0].mutable {
		t.Fatalf("optional mutable reference lost mutable loan kind")
	}

	clear := inspectFunctionAnalysis(t, result, "clear")
	clearFn := result.module.AST.Stmts[2].(*ast.FnDecl)
	marker := analysisNodeForStmt(t, clear, clearFn.Body.Stmts[2])
	maybeCleared, _ := clear.functionScope.Lookup("maybe")
	if _, tracked := clear.inStates[marker.flow.ID].references[maybeCleared]; tracked {
		t.Fatalf("none assignment retained optional reference origins")
	}

	parameter := inspectFunctionAnalysis(t, result, "parameter")
	parameterFn := result.module.AST.Stmts[3].(*ast.FnDecl)
	parameterUse := analysisNodeForStmt(t, parameter, parameterFn.Body.Stmts[0])
	maybeParameter, _ := parameter.functionScope.Lookup("maybe")
	parameterValue := parameter.inStates[parameterUse.flow.ID].references[maybeParameter]
	if !place.SameOrigins(referenceOrigins(parameterValue), []place.Origin{{Root: maybeParameter}}) {
		t.Fatalf("optional reference parameter origins = %#v", referenceOrigins(parameterValue))
	}
	if _, live := parameter.referenceLiveIn[parameterUse.flow.ID][maybeParameter]; !live {
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
	selectedValue := analysis.inStates[copyNode.flow.ID].references[selected]
	if got := referenceOrigins(selectedValue); !place.SameOrigins(got, want) {
		t.Fatalf("joined origins = %#v, want %#v", got, want)
	}
	if len(selectedValue) != 2 {
		t.Fatalf("joined reference collapsed %d distinct loans, want 2", len(selectedValue))
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
	if _, live := analysis.referenceLiveIn[callNode.flow.ID][reference]; !live {
		t.Fatalf("reference not live at its final use")
	}
	if _, live := analysis.referenceLiveOut[callNode.flow.ID][reference]; live {
		t.Fatalf("reference remains live after final use")
	}
	if _, live := analysis.referenceLiveIn[assignNode.flow.ID][reference]; live {
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
	if _, live := analysis.referenceLiveIn[conditionNode.flow.ID][maybe]; !live {
		t.Fatalf("reference not live at conditional use")
	}
	if _, live := analysis.referenceLiveOut[conditionNode.flow.ID][maybe]; live {
		t.Fatalf("reference remains live after conditional use")
	}
	if _, live := analysis.referenceLiveIn[thenNode.flow.ID][maybe]; live {
		t.Fatalf("reference remains live in then branch")
	}
	if _, live := analysis.referenceLiveIn[elseNode.flow.ID][maybe]; live {
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
	if _, live := analysis.referenceLiveIn[loopNode.flow.ID][reference]; !live {
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
	var exit *site
	for _, node := range analysis.sites {
		if node != nil && node.flow != nil && node.flow.Kind == cfg.SiteScopeExit && node.block == fn.Body {
			exit = node
			break
		}
	}
	if exit == nil {
		t.Fatalf("loop exit continuation not found")
	}
	maybe, _ := analysis.functionScope.Lookup("maybe")
	if _, live := analysis.referenceLiveIn[exit.flow.ID][maybe]; live {
		t.Fatalf("loop exit continuation repeats condition use")
	}
}

func TestBorrowCompatibleAccesses(t *testing.T) {
	tests := map[string]string{
		"shared borrows": `fn Read(_: &i32) {}
fn valid(value: i32) {
	let first = &value;
	let second = &value;
	Read(first);
	Read(second);
}`,
		"optional shared borrows": `fn Both(_: ?&i32, _: &i32) {}
fn valid(value: i32) {
	Both(&value, &value);
	Both(none, &value);
}`,
		"disjoint fields": `struct Pair { left: i32, right: i32 }
fn Write(_: &mut i32) {}
fn valid(mut pair: Pair) {
	let left = &mut pair.left;
	pair.right = 2;
	Write(left);
}`,
		"disjoint fixed indexes": `fn Write(_: &mut i32) {}
fn valid(mut values: [2]i32) {
	let first = &mut values[0];
	values[1] = 2;
	Write(first);
}`,
		"after final use": `fn Read(_: &i32) {}
fn valid(mut value: i32) {
	let reference = &value;
	Read(reference);
	value = 2;
}`,
		"through mutable reference": `struct Cell { value: i32 }
fn valid(reference: &mut Cell) -> i32 {
	reference.value = 2;
	return reference.value;
}`,
		"sequential mutable reborrows": `fn Write(_: &mut i32) {}
fn valid(reference: &mut i32) {
	Write(reference);
	Write(reference);
}`,
		"nested read before activation": `fn Read(_: &i32) -> i32 { return 0; }
fn Both(_: &mut i32, _: i32) {}
fn valid(mut value: i32) {
	Both(&mut value, Read(&value));
}`,
		"indirect callable nested read": `fn Read(_: &i32) -> i32 { return 0; }
fn valid(call: fn(&mut i32, i32), mut value: i32) {
	call(&mut value, Read(&value));
}`,
		"final reference use before activation": `fn Read(_: &i32) -> i32 { return 0; }
fn Both(_: &mut i32, _: i32) {}
fn valid(mut value: i32) {
	let reference = &value;
	Both(&mut value, Read(reference));
}`,
		"final reference use in borrow index": `fn Index(_: &mut [2]i32) -> usize { return 0; }
fn Write(_: &mut i32) {}
fn valid() {
	let mut values = [_]i32{1, 2};
	let reference = &mut values;
	Write(&mut values[Index(reference)]);
}`,
		"mutable reference reborrow before activation": `fn Read(_: &i32) -> i32 { return 0; }
fn Both(_: &mut i32, _: i32) {}
fn valid(reference: &mut i32) {
	Both(reference, Read(reference));
}`,
		"optional mutable nested read": `fn Read(_: &i32) -> i32 { return 0; }
fn Both(_: ?&mut i32, _: i32) {}
fn valid(mut value: i32) {
	Both(&mut value, Read(&value));
	Both(none, Read(&value));
}`,
		"mutable receiver nested read": `struct Counter { value: i32 }
fn (self: &Counter) Current() -> i32 { return self.value; }
fn (self: &mut Counter) Add(_: i32) {}
fn valid(mut value: Counter) {
	value.Add(value.Current());
}`,
		"disjoint call reservations": `struct Pair { left: i32, right: i32 }
fn Both(_: &mut i32, _: &mut i32) {}
fn valid(mut pair: Pair) {
	Both(&mut pair.left, &mut pair.right);
}`,
		"disjoint fixed index reservations": `fn Both(_: &mut i32, _: &mut i32) {}
fn valid(mut values: [2]i32) {
	Both(&mut values[0], &mut values[1]);
}`,
		"raw address outside safe loans": `fn Keep(_: rawptr) {}
fn Read(_: &mut i32) {}
fn valid(mut value: i32) {
	let reference = &mut value;
	Keep(@value);
	Read(reference);
}`,
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			result := checkOwnershipSource(t, src)
			if result.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
			}
		})
	}
}

func TestBorrowConflictingAccesses(t *testing.T) {
	tests := map[string]string{
		"shared during mutable borrow": `fn Read(_: &i32) {}
fn bad(mut value: i32) {
	let exclusive = &mut value;
	Read(&value);
	Read(exclusive);
}`,
		"mutation during shared borrow": `fn Read(_: &i32) {}
fn bad(mut value: i32) {
	let reference = &value;
	value = 2;
	Read(reference);
}`,
		"same call aliases": `fn Both(_: &mut i32, _: &i32) {}
fn bad(mut value: i32) {
	Both(&mut value, &value);
}`,
		"optional mutable call argument": `fn Both(_: ?&mut i32, _: &i32) {}
fn bad(mut value: i32) {
	Both(&mut value, &value);
}`,
		"optional shared call argument": `fn Both(_: ?&i32, _: &mut i32) {}
fn bad(mut value: i32) {
	Both(&value, &mut value);
}`,
		"nested mutable activation": `fn Write(_: &mut i32) -> i32 { return 0; }
fn Both(_: &mut i32, _: i32) {}
fn bad(mut value: i32) {
	Both(&mut value, Write(&mut value));
}`,
		"overlapping reservations": `fn Both(_: &mut i32, _: &mut i32) {}
fn bad(mut value: i32) {
	Both(&mut value, &mut value);
}`,
		"dynamic index reservations": `fn Both(_: &mut i32, _: &mut i32) {}
fn bad(mut values: [2]i32, left: usize, right: usize) {
	Both(&mut values[left], &mut values[right]);
}`,
		"consume during reservation": `struct Box { value: i32 }
fn Consume(_: Box) -> i32 { return 0; }
fn Both(_: &mut Box, _: i32) {}
fn bad(mut owner: Box) {
	Both(&mut owner, Consume(owner));
}`,
		"shared loan live after activation": `fn Read(_: &i32) -> i32 { return 0; }
fn Keep(_: &i32) {}
fn Both(_: &mut i32, _: i32) {}
fn bad(mut value: i32) {
	let reference = &value;
	Both(&mut value, Read(reference));
	Keep(reference);
}`,
		"mutable method receiver": `struct Counter { value: i32 }
fn (self: &mut Counter) Mix(_: &Counter) {}
fn bad(mut value: Counter) {
	value.Mix(&value);
}`,
		"child reborrow": `struct Cell { value: i32 }
fn Write(_: &mut i32, _: i32) {}
fn bad(parent: &mut Cell) {
	let child = &mut parent.value;
	let current = parent.value;
	Write(child, current);
}`,
		"conditional origin union": `fn Read(_: &i32) {}
fn bad(cond: bool, mut left: i32, right: i32) {
	let mut selected = &left;
	if cond {
		selected = &right;
	}
	left = 3;
	Read(selected);
}`,
		"borrowed local scope exit": `fn Read(_: &i32) {}
fn bad(seed: i32) {
	let mut escaped = &seed;
	{
		let local = 1;
		escaped = &local;
	}
	Read(escaped);
}`,
		"owner move": `struct Box { value: i32 }
fn Consume(_: Box) {}
fn Read(_: &Box) {}
fn bad(owner: Box) {
	let reference = &owner;
	Consume(owner);
	Read(reference);
}`,
		"owner free": `fn Read(_: &*i32) {}
fn bad(owner: *i32) {
	let reference = &owner;
	free(owner);
	Read(reference);
}`,
		"slice view source": `fn Read(_: &[..]i32) {}
fn bad(mut values: [2]i32) {
	let view = values[..];
	values[0] = 3;
	Read(view);
}`,
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			result := checkOwnershipSource(t, src)
			if !hasOwnershipCode(result, diagnostics.ErrBorrowConflict) {
				t.Fatalf("expected borrow-conflict diagnostic, got:\n%s", result.EmitAllToString())
			}
		})
	}
}

func TestBorrowConflictDiagnosticShowsLoanLifetime(t *testing.T) {
	result := checkOwnershipSource(t, `fn Both(_: &mut i32, _: &i32) {}
fn bad(mut value: i32) {
	Both(&mut value, &value);
}`)
	for _, item := range result.Diagnostics() {
		if item == nil || item.Code != diagnostics.ErrBorrowConflict {
			continue
		}
		if len(item.Labels) != 3 {
			t.Fatalf("borrow diagnostic labels = %d, want activation, reservation, and conflict", len(item.Labels))
		}
		if !strings.Contains(item.Labels[0].Message, "activates here") ||
			!strings.Contains(item.Labels[1].Message, "reserved here") ||
			!strings.Contains(item.Labels[2].Message, "borrow created here") {
			t.Fatalf("unexpected borrow diagnostic labels: %#v", item.Labels)
		}
		return
	}
	t.Fatalf("expected borrow-conflict diagnostic, got:\n%s", result.EmitAllToString())
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

func TestDynamicArrayOwnerOperationsBorrowAndKeepOwnerLive(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let mut values = []i32{};
	values |> append(1);
	values |> reserve(8);
	values |> resize(4, 0);
	values |> shrink(2);
	print(values[0]);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayShrinkDoesNotConsumeOwner(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let mut values = []i32{1};
	values |> shrink(0);
	print(values |> len());
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected borrowed-owner diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayAppendDoesNotConsumeOwner(t *testing.T) {
	diag := checkOwnershipSource(t, `fn main() {
	let mut values = []i32{};
	values |> append(1);
	print(values[0]);
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected borrowed-owner diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestDynamicArrayAppendConsumesCompositeElement(t *testing.T) {
	diag := checkOwnershipSource(t, `struct Point { x: i32 }
fn consume(point: Point) {}
fn main() {
	let point = .Point{x = 1};
	let mut values = []Point{};
	values |> append(point);
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

func TestReferenceReturnOriginsAcceptDeclaredParameterPaths(t *testing.T) {
	result := checkOwnershipSource(t, `
fn choose(cond: bool, left: &i32, right: &i32) -> &i32 from(left, right) {
	if cond {
		return left;
	}
	return right;
}

fn recursive(value: &i32, done: bool) -> &i32 from value {
	if done {
		return value;
	}
	return recursive(value, true);
}
`)
	if result.HasErrors() {
		t.Fatalf("unexpected declared-origin diagnostics:\n%s", result.EmitAllToString())
	}
}

func TestReferenceReturnRejectsOriginOutsideContract(t *testing.T) {
	result := checkOwnershipSource(t, `
fn wrong(left: &i32, right: &i32) -> &i32 from left {
	return right;
}

fn local(source: &i32) -> &i32 from source {
	let value: i32 = 1;
	return &value;
}
`)
	if !hasOwnershipCode(result, diagnostics.ErrInvalidReturn) ||
		strings.Count(result.EmitAllToString(), "outside declared `from` sources") != 2 {
		t.Fatalf("expected undeclared return-origin diagnostics:\n%s", result.EmitAllToString())
	}
}

func TestReferenceReturningCallExtendsSourceLoan(t *testing.T) {
	result := checkOwnershipSource(t, `
fn identity(value: &i32) -> &i32 from value {
	return value;
}

fn Read(_: &i32) {}

fn bad(mut value: i32) {
	let reference = identity(&value);
	value = 2;
	Read(reference);
}
`)
	if !hasOwnershipCode(result, diagnostics.ErrBorrowConflict) {
		t.Fatalf("expected returned-call loan conflict:\n%s", result.EmitAllToString())
	}
}

func TestReferenceReturningCallLoanEndsAfterLastUse(t *testing.T) {
	result := checkOwnershipSource(t, `
fn identity(value: &i32) -> &i32 from value {
	return value;
}

fn Read(_: &i32) {}

fn valid(mut value: i32) {
	let reference = identity(&value);
	Read(reference);
	value = 2;
}
`)
	if result.HasErrors() {
		t.Fatalf("unexpected returned-call loan diagnostics:\n%s", result.EmitAllToString())
	}
}
