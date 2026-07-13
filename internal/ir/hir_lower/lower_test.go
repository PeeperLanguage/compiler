package hir_lower

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/ownership"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

func generateTestHIR(t *testing.T, filePath, importPath, src string) *hir.Module {
	t.Helper()
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: importPath,
		FilePath:   filePath,
		Content:    src,
		AST:        parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	typechecker.Check(ctx, module)
	ownership.Check(ctx, module)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	return GenerateHIR(ctx, module)
}

func TestGenerateHIRLowersIndexExpr(t *testing.T) {
	const filePath = "hir_index_test" + peeper.SourceExt
	src := `fn first(xs: [4]i32) -> i32 {
	return xs[0];
}`
	out := generateTestHIR(t, filePath, "hir_index_test", src)
	if out == nil || len(out.Funcs) != 1 || out.Funcs[0].Body == nil || len(out.Funcs[0].Body.Stmts) != 1 {
		t.Fatalf("unexpected HIR shape: %#v", out)
	}
	ret, ok := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	if !ok {
		t.Fatalf("expected return stmt, got %#v", out.Funcs[0].Body.Stmts[0])
	}
	index, ok := ret.Value.(*ir.Index)
	if !ok {
		t.Fatalf("expected index expr, got %#v", ret.Value)
	}
	if index.TypeText() != "i32" {
		t.Fatalf("index type = %q, want i32", index.TypeText())
	}
}

func TestGenerateHIRLowersSliceViewIndexExpr(t *testing.T) {
	const filePath = "hir_slice_view_index_test" + peeper.SourceExt
	src := `fn first(xs: &[]i32, index: usize) -> i32 {
	return xs[index];
}`
	out := generateTestHIR(t, filePath, "hir_slice_view_index_test", src)
	if out == nil || len(out.Funcs) != 1 || out.Funcs[0].Body == nil || len(out.Funcs[0].Body.Stmts) != 1 {
		t.Fatalf("unexpected HIR shape: %#v", out)
	}
	ret, ok := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	if !ok {
		t.Fatalf("expected return stmt, got %#v", out.Funcs[0].Body.Stmts[0])
	}
	index, ok := ret.Value.(*ir.Index)
	if !ok || index.TypeText() != "i32" {
		t.Fatalf("expected i32 index expr, got %#v", ret.Value)
	}
}

func TestGenerateHIRLowersFixedArrayRangeAsMutableSliceView(t *testing.T) {
	const filePath = "hir_range_slice_test" + peeper.SourceExt
	src := `fn slice(mut xs: [4]i32) {
	let view = xs[1..=2];
}`
	out := generateTestHIR(t, filePath, "hir_range_slice_test", src)
	binding, ok := out.Funcs[0].Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected range binding, got %#v", out.Funcs[0].Body.Stmts[0])
	}
	view, ok := binding.Value.(*ir.SliceView)
	if !ok || view.TypeText() != "&mut []i32" || view.EndExclusive {
		t.Fatalf("expected inclusive mutable SliceView, got %#v", binding.Value)
	}
	if source, ok := view.Source.(*ir.AddrOf); !ok || source.TypeText() != "&mut [4]i32" {
		t.Fatalf("expected fixed-array place address, got %#v", view.Source)
	}
	start, startOK := view.Start.(*ir.IntLit)
	end, endOK := view.End.(*ir.IntLit)
	if !startOK || !endOK || start.Value != "1" || end.Value != "2" {
		t.Fatalf("unexpected range bounds: %#v..%#v", view.Start, view.End)
	}
}

func TestGenerateHIRLowersConstIndexExpr(t *testing.T) {
	const filePath = "hir_const_index_test" + peeper.SourceExt
	src := `const I: i32 = 1;

fn first(xs: [4]i32) -> i32 {
	return xs[I];
}`
	out := generateTestHIR(t, filePath, "hir_const_index_test", src)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	index, ok := ret.Value.(*ir.Index)
	if !ok {
		t.Fatalf("expected index expr, got %#v", ret.Value)
	}
	lit, ok := index.Index.(*ir.IntLit)
	if !ok || lit.Value != "1" {
		t.Fatalf("index = %#v, want literal 1", index.Index)
	}
}

func TestGenerateHIRMaterializesNumericWidening(t *testing.T) {
	out := generateTestHIR(t, "hir_numeric_widen_test"+peeper.SourceExt, "hir_numeric_widen_test", `fn widen(value: i8) -> u16 {
	return value;
}`)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	cast, ok := ret.Value.(*ir.Cast)
	if !ok || cast.TypeText() != "u16" || cast.Expr.TypeText() != "i8" {
		t.Fatalf("widening return = %#v, want i8-to-u16 cast", ret.Value)
	}
}

func TestGenerateHIRLowersFreeAsDrop(t *testing.T) {
	out := generateTestHIR(t, "hir_free_test"+peeper.SourceExt, "hir_free_test", `fn destroy(value: *i32) {
	free(value);
}`)
	stmt, ok := out.Funcs[0].Body.Stmts[0].(*hir.ExprStmt)
	if !ok {
		t.Fatalf("expected expression statement, got %#v", out.Funcs[0].Body.Stmts[0])
	}
	if _, ok := stmt.Value.(*ir.Drop); !ok {
		t.Fatalf("expected drop expression, got %#v", stmt.Value)
	}
}

func TestGenerateHIRDropsDiscardedOwnerReturningCall(t *testing.T) {
	out := generateTestHIR(t, "hir_discarded_owner_call_test"+peeper.SourceExt, "hir_discarded_owner_call_test", `fn acquire() -> *i32;
fn main() {
	acquire();
}`)
	stmt, ok := out.Funcs[0].Body.Stmts[0].(*hir.ExprStmt)
	if !ok {
		t.Fatalf("expected expression statement, got %#v", out.Funcs[0].Body.Stmts[0])
	}
	drop, ok := stmt.Value.(*ir.Drop)
	if !ok {
		t.Fatalf("expected drop expression, got %#v", stmt.Value)
	}
	if _, ok := drop.Value.(*ir.Call); !ok {
		t.Fatalf("expected dropped call, got %#v", drop.Value)
	}
}

func TestGenerateHIRDropsDiscardedOwnedCompositeTemporaries(t *testing.T) {
	out := generateTestHIR(t, "hir_discarded_owned_composite_test"+peeper.SourceExt, "hir_discarded_owned_composite_test", `struct Box { ptr: *i32 }
fn acquire() -> *i32;
fn main() {
	.Box{ ptr = acquire() };
	[1]*i32{acquire()};
}`)
	if len(out.Funcs[0].Body.Stmts) != 2 {
		t.Fatalf("expected two expression statements, got %#v", out.Funcs[0].Body.Stmts)
	}
	for index, stmt := range out.Funcs[0].Body.Stmts {
		exprStmt, ok := stmt.(*hir.ExprStmt)
		if !ok {
			t.Fatalf("statement %d = %#v, want expression statement", index, stmt)
		}
		if _, ok := exprStmt.Value.(*ir.Drop); !ok {
			t.Fatalf("statement %d value = %#v, want dropped temporary", index, exprStmt.Value)
		}
	}
}

func TestGenerateHIRDoesNotDropDiscardedOwnedPlace(t *testing.T) {
	out := generateTestHIR(t, "hir_discarded_owned_place_test"+peeper.SourceExt, "hir_discarded_owned_place_test", `fn keep(value: *i32) {
	value;
}`)
	stmt := out.Funcs[0].Body.Stmts[0].(*hir.ExprStmt)
	if _, ok := stmt.Value.(*ir.Drop); ok {
		t.Fatalf("discarded place must remain live, got %#v", stmt.Value)
	}
}

func TestGenerateHIRMarksOwnerBearingProjectionBaseForDrop(t *testing.T) {
	out := generateTestHIR(t, "hir_projected_owner_temporary_test"+peeper.SourceExt, "hir_projected_owner_temporary_test", `struct Box { value: i32, ptr: *i32 }
fn make() -> Box;
fn read() -> i32 {
	return make().value;
}`)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	field, ok := ret.Value.(*ir.Field)
	if !ok || !field.DropBase {
		t.Fatalf("expected projected call temporary cleanup, got %#v", ret.Value)
	}
}

func TestGenerateHIRKeepsOwnerBearingProjectionPlaceLive(t *testing.T) {
	out := generateTestHIR(t, "hir_projected_owner_place_test"+peeper.SourceExt, "hir_projected_owner_place_test", `struct Box { value: i32, ptr: *i32 }
fn read(box: Box) -> i32 {
	return box.value;
}`)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	field, ok := ret.Value.(*ir.Field)
	if !ok || field.DropBase {
		t.Fatalf("named projection base must remain live, got %#v", ret.Value)
	}
}

func TestGenerateHIRLowersArrayLiteral(t *testing.T) {
	const filePath = "hir_array_lit_test" + peeper.SourceExt
	src := `fn first() -> [3]i32 {
	return [_]i32{1, 2, 3};
}`
	out := generateTestHIR(t, filePath, "hir_array_lit_test", src)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	lit, ok := ret.Value.(*ir.ArrayLit)
	if !ok {
		t.Fatalf("expected array literal, got %#v", ret.Value)
	}
	if lit.TypeText() != "[3]i32" || len(lit.Values) != 3 {
		t.Fatalf("unexpected array literal lowering: type=%q values=%d", lit.TypeText(), len(lit.Values))
	}
}

func TestGenerateHIRLowersDynamicArrayBorrowsAsSliceViews(t *testing.T) {
	const filePath = "hir_slice_view_test" + peeper.SourceExt
	const src = `struct Bucket {
	items: []i32
}

fn explicit(xs: []i32) {
	let _ = &xs;
}

fn explicit_mutable(mut xs: []i32) {
	let _ = &mut xs;
}

fn nested(mut bucket: Bucket) {
	let _ = &mut bucket.items;
}`
	out := generateTestHIR(t, filePath, "hir_slice_view_test", src)
	funcs := make(map[string]*hir.Function, len(out.Funcs))
	for _, fn := range out.Funcs {
		funcs[fn.Name] = fn
	}

	explicit := funcs["explicit"]
	if explicit == nil || explicit.Body == nil || len(explicit.Body.Stmts) < 1 {
		t.Fatalf("unexpected explicit borrow HIR: %#v", explicit)
	}
	binding, ok := explicit.Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected explicit borrow binding, got %#v", explicit.Body.Stmts[0])
	}
	if view, ok := binding.Value.(*ir.SliceView); !ok || view.TypeText() != "&[]i32" {
		t.Fatalf("expected shared SliceView, got %#v", binding.Value)
	}

	explicitMutable := funcs["explicit_mutable"]
	if explicitMutable == nil || explicitMutable.Body == nil || len(explicitMutable.Body.Stmts) < 1 {
		t.Fatalf("unexpected explicit mutable borrow HIR: %#v", explicitMutable)
	}
	binding, ok = explicitMutable.Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected explicit mutable borrow binding, got %#v", explicitMutable.Body.Stmts[0])
	}
	if view, ok := binding.Value.(*ir.SliceView); !ok || view.TypeText() != "&mut []i32" {
		t.Fatalf("expected mutable SliceView, got %#v", binding.Value)
	}

	nested := funcs["nested"]
	if nested == nil || nested.Body == nil || len(nested.Body.Stmts) < 1 {
		t.Fatalf("unexpected nested mutable borrow HIR: %#v", nested)
	}
	binding, ok = nested.Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected nested borrow binding, got %#v", nested.Body.Stmts[0])
	}
	view, ok := binding.Value.(*ir.SliceView)
	if !ok || view.TypeText() != "&mut []i32" {
		t.Fatalf("expected nested mutable SliceView, got %#v", binding.Value)
	}
	field, ok := view.Source.(*ir.Field)
	if !ok || !field.ThroughPtr {
		t.Fatalf("expected nested view from original field place, got %#v", view.Source)
	}
}

func TestGenerateHIRLowersAddressAsOpaqueRawPointer(t *testing.T) {
	const filePath = "hir_raw_pointer_address_test" + peeper.SourceExt
	const src = `fn explicit(value: i32) {
	let _ = @value;
}`
	out := generateTestHIR(t, filePath, "hir_raw_pointer_address_test", src)
	funcs := make(map[string]*hir.Function, len(out.Funcs))
	for _, fn := range out.Funcs {
		funcs[fn.Name] = fn
	}

	explicit := funcs["explicit"]
	if explicit == nil || explicit.Body == nil || len(explicit.Body.Stmts) != 1 {
		t.Fatalf("unexpected explicit raw pointer HIR: %#v", explicit)
	}
	binding, ok := explicit.Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected explicit raw pointer binding, got %#v", explicit.Body.Stmts[0])
	}
	if address, ok := binding.Value.(*ir.AddrOf); !ok || address.TypeText() != "rawptr" {
		t.Fatalf("expected raw pointer AddrOf, got %#v", binding.Value)
	}
}

func TestGenerateHIRMarksConsumingInterfaceCall(t *testing.T) {
	const src = `iface Consumer { fn (Self) take() -> i32 }
struct Counter { value: i32 }
fn (self: Counter) take() -> i32 { return self.value; }
fn consume(counter: *Counter) -> i32 {
	let consumer: *Consumer = counter;
	return consumer.take();
}`
	out := generateTestHIR(t, "hir_consuming_interface_test"+peeper.SourceExt, "hir_consuming_interface_test", src)
	var consume *hir.Function
	for _, fn := range out.Funcs {
		if fn.Name == "consume" {
			consume = fn
			break
		}
	}
	if consume == nil || consume.Body == nil || len(consume.Body.Stmts) != 2 {
		t.Fatalf("unexpected consuming interface HIR: %#v", consume)
	}
	ret, ok := consume.Body.Stmts[1].(*hir.Return)
	if !ok {
		t.Fatalf("expected return, got %#v", consume.Body.Stmts[1])
	}
	call, ok := ret.Value.(*ir.InterfaceCall)
	if !ok || !call.Consumes {
		t.Fatalf("expected consuming interface call marker, got %#v", ret.Value)
	}
}

func TestGenerateHIRSupportsInterfaceCarrierMethodParameters(t *testing.T) {
	const src = `iface Reader { fn (&Self) read() -> i32 }
iface Consumer { fn (&Self) consume(value: &Reader) -> i32 }
struct Point { value: i32 }
struct ConsumerImpl {}
fn (self: &Point) read() -> i32 { return self.value; }
fn (_: &ConsumerImpl) consume(value: &Reader) -> i32 { return value.read(); }
fn use(consumer: &Consumer, reader: &Reader) -> i32 { return consumer.consume(reader); }
fn main() -> i32 {
	let consumer: ConsumerImpl = .{};
	let point: Point = .{ value = 7 };
	return use(&consumer, &point);
}`
	out := generateTestHIR(t, "hir_interface_parameter_test"+peeper.SourceExt, "hir_interface_parameter_test", src)
	var mainFn *hir.Function
	for _, fn := range out.Funcs {
		if fn.Name == "main" {
			mainFn = fn
			break
		}
	}
	if mainFn == nil || mainFn.Body == nil || len(mainFn.Body.Stmts) != 3 {
		t.Fatalf("unexpected main HIR: %#v", mainFn)
	}
	ret, ok := mainFn.Body.Stmts[2].(*hir.Return)
	if !ok {
		t.Fatalf("expected return, got %#v", mainFn.Body.Stmts[2])
	}
	call, ok := ret.Value.(*ir.Call)
	if !ok || len(call.Args) != 2 {
		t.Fatalf("expected use call, got %#v", ret.Value)
	}
	consumer, ok := call.Args[0].(*ir.InterfaceMake)
	if !ok || len(consumer.Slots) != 1 || consumer.Slots[0].SlotType == "" {
		t.Fatalf("expected interface carrier slot, got %#v", call.Args[0])
	}
}
