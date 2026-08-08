package hir_lower

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/cfg"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/ownership"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typechecker"
	"compiler/internal/semantics/typeinfo"
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
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	out := GenerateHIR(ctx, module)
	module.HIR = out
	module.CFG = cfg.BuildModule(out)
	ownership.Check(ctx, module)
	return out
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
	load, ok := ret.Value.(*ir.Load)
	if !ok || load.Place == nil || len(load.Place.Projections) != 1 {
		t.Fatalf("expected indexed place load, got %#v", ret.Value)
	}
	if out.Types.Text(load.TypeID()) != "i32" || load.Place.Projections[0].Kind != ir.PlaceProjectionIndex {
		t.Fatalf("index load = %#v, want i32 Index place", load)
	}
}

func TestGenerateHIRPreservesSourceAndSymbolIdentity(t *testing.T) {
	out := generateTestHIR(t, "hir_identity_test"+peeper.SourceExt, "hir_identity_test", `fn echo(value: i32) -> i32 {
	let copy = value;
	copy;
	return copy;
}`)
	fn := out.Funcs[0]
	if fn.NodeID == 0 || fn.SymbolID == 0 || fn.Body.NodeID == 0 || len(fn.Params) != 1 || fn.Params[0].SymbolID == 0 {
		t.Fatalf("function identity = %#v, want source and symbol IDs", fn)
	}
	binding, ok := fn.Body.Stmts[0].(*hir.Binding)
	if !ok || binding.NodeID == 0 || binding.SymbolID == 0 {
		t.Fatalf("binding identity = %#v, want source and symbol IDs", fn.Body.Stmts[0])
	}
	paramUse, ok := binding.Value.(*ir.Ident)
	if !ok || paramUse.SymbolID != fn.Params[0].SymbolID {
		t.Fatalf("binding value = %#v, want parameter symbol %d", binding.Value, fn.Params[0].SymbolID)
	}
	discarded, ok := fn.Body.Stmts[1].(*hir.ExprStmt)
	if !ok || discarded.NodeID == 0 || discarded.ValueNodeID == 0 {
		t.Fatalf("discarded expression = %#v, want statement and value source IDs", fn.Body.Stmts[1])
	}
	ret, ok := fn.Body.Stmts[2].(*hir.Return)
	if !ok || ret.NodeID == 0 {
		t.Fatalf("return identity = %#v, want source ID", fn.Body.Stmts[2])
	}
	bindingUse, ok := ret.Value.(*ir.Ident)
	if !ok || bindingUse.SymbolID != binding.SymbolID {
		t.Fatalf("return value = %#v, want binding symbol %d", ret.Value, binding.SymbolID)
	}
}

func TestGenerateHIRLowersDynamicArrayOwnerOperations(t *testing.T) {
	const src = `fn main() {
	let appended = append([]i32{}, 1);
	let reserved = reserve(appended, 8);
	let resized = resize(reserved, 4, 0);
	let shrunk = shrink(resized, 2);
}`
	out := generateTestHIR(t, "hir_dynamic_array_ops_test"+peeper.SourceExt, "hir_dynamic_array_ops_test", src)
	want := []symbols.CompilerOp{symbols.CompilerOpAppend, symbols.CompilerOpReserve, symbols.CompilerOpResize, symbols.CompilerOpShrink}
	if len(out.Funcs[0].Body.Stmts) < len(want) {
		t.Fatalf("operation statements = %d, want at least %d", len(out.Funcs[0].Body.Stmts), len(want))
	}
	for i := range want {
		stmt := out.Funcs[0].Body.Stmts[i]
		binding, ok := stmt.(*hir.Binding)
		if !ok {
			t.Fatalf("stmt %d = %#v, want binding", i, stmt)
		}
		op, ok := binding.Value.(*ir.DynamicArrayOp)
		if !ok || op.Op != want[i] || out.Types.Text(op.Type) != "[]i32" {
			t.Fatalf("stmt %d operation = %#v, want %s []i32", i, binding.Value, want[i])
		}
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
	load, ok := ret.Value.(*ir.Load)
	if !ok || out.Types.Text(load.TypeID()) != "i32" || load.Place == nil || len(load.Place.Projections) != 1 || load.Place.Projections[0].Kind != ir.PlaceProjectionIndex {
		t.Fatalf("expected i32 indexed place load, got %#v", ret.Value)
	}
}

func TestGenerateHIRPreservesFixedArrayFieldForIndexedMutableBorrow(t *testing.T) {
	const src = `struct Token { value: i32 }
struct Holder { tokens: [1]Token }
fn update(mut holder: Holder) {
	let _ = &mut holder.tokens[0];
}`
	out := generateTestHIR(t, "hir_indexed_field_borrow_test"+peeper.SourceExt, "hir_indexed_field_borrow_test", src)
	binding, ok := out.Funcs[0].Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected reference binding, got %#v", out.Funcs[0].Body.Stmts[0])
	}
	address, ok := binding.Value.(*ir.AddrOf)
	if !ok || out.Types.Text(address.Type) != "&mut struct{value: i32}" {
		t.Fatalf("expected mutable element address, got %#v", binding.Value)
	}
	if address.Place == nil || out.Types.Text(address.Place.Root.TypeID()) != "struct{tokens: [1]struct{value: i32}}" {
		t.Fatalf("expected Holder place root, got %#v", address.Place)
	}
	if len(address.Place.Projections) != 2 {
		t.Fatalf("expected Field/Index projections, got %#v", address.Place.Projections)
	}
	if address.Place.Projections[0].Kind != ir.PlaceProjectionField || address.Place.Projections[1].Kind != ir.PlaceProjectionIndex {
		t.Fatalf("expected original Field/Index place, got %#v", address.Place.Projections)
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
	if !ok || out.Types.Text(view.Type) != "&mut []i32" || view.EndExclusive {
		t.Fatalf("expected inclusive mutable SliceView, got %#v", binding.Value)
	}
	if view.Source == nil || out.Types.Text(view.Source.Type) != "[4]i32" || len(view.Source.Projections) != 0 {
		t.Fatalf("expected fixed-array source place, got %#v", view.Source)
	}
	start, startOK := view.Start.(*ir.IntLit)
	end, endOK := view.End.(*ir.IntLit)
	if !startOK || !endOK || start.Value != "1" || end.Value != "2" {
		t.Fatalf("unexpected range bounds: %#v..%#v", view.Start, view.End)
	}
}

func TestGenerateHIRLowersStringCharsIntrinsic(t *testing.T) {
	src := `fn main() -> i32 {
	let text: str = "aé";
	let chars = text.as_chars();
	return chars.len() as i32;
}`
	out := generateTestHIR(t, "hir_string_chars_test"+peeper.SourceExt, "hir_string_chars_test", src)
	var chars *ir.StringChars
	for _, stmt := range out.Funcs[0].Body.Stmts {
		binding, ok := stmt.(*hir.Binding)
		if !ok {
			continue
		}
		if value, ok := binding.Value.(*ir.StringChars); ok {
			chars = value
			break
		}
	}
	if chars == nil || out.Types.Text(chars.TypeID()) != "[]char" {
		t.Fatalf("expected StringChars returning []char, got %#v", chars)
	}
}

func TestGenerateHIRPreservesTemporaryStringViewOwners(t *testing.T) {
	src := `fn Make() -> str { return "abc"; }
fn main() -> i32 {
	let byte = Make().as_bytes()[0];
	let size = Make()[0..1].len();
	return byte as i32 + size as i32;
}`
	out := generateTestHIR(t, "hir_temporary_string_views_test"+peeper.SourceExt, "hir_temporary_string_views_test", src)
	mainFn := out.Funcs[1]
	byteBinding := mainFn.Body.Stmts[0].(*hir.Binding)
	byteLoad := byteBinding.Value.(*ir.Load)
	byteView := byteLoad.Place.Root.(*ir.SliceView)
	if _, ok := byteView.Source.Root.(*ir.TempBorrow); !ok {
		t.Fatalf("temporary as_bytes source = %T, want TempBorrow", byteView.Source.Root)
	}

	sizeBinding := mainFn.Body.Stmts[1].(*hir.Binding)
	rangeLen := sizeBinding.Value.(*ir.Len)
	rangeView := rangeLen.Value.(*ir.SliceView)
	if _, ok := rangeView.Source.Root.(*ir.TempBorrow); !ok {
		t.Fatalf("temporary string range source = %T, want TempBorrow", rangeView.Source.Root)
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
	load, ok := ret.Value.(*ir.Load)
	if !ok || load.Place == nil || len(load.Place.Projections) != 1 {
		t.Fatalf("expected index place load, got %#v", ret.Value)
	}
	lit, ok := load.Place.Projections[0].Index.(*ir.IntLit)
	if !ok || lit.Value != "1" {
		t.Fatalf("index = %#v, want literal 1", load.Place.Projections[0].Index)
	}
}

func TestGenerateHIRMaterializesNumericWidening(t *testing.T) {
	out := generateTestHIR(t, "hir_numeric_widen_test"+peeper.SourceExt, "hir_numeric_widen_test", `fn widen(value: i8) -> u16 {
	return value;
}`)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	cast, ok := ret.Value.(*ir.Cast)
	if !ok || out.Types.Text(cast.Type) != "u16" || out.Types.Text(cast.Expr.TypeID()) != "i8" {
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

func TestGenerateHIRPreservesDiscardedOwnerReturningCallIdentity(t *testing.T) {
	out := generateTestHIR(t, "hir_discarded_owner_call_test"+peeper.SourceExt, "hir_discarded_owner_call_test", `fn acquire() -> *i32;
fn main() {
	acquire();
}`)
	stmt, ok := out.Funcs[0].Body.Stmts[0].(*hir.ExprStmt)
	if !ok {
		t.Fatalf("expected expression statement, got %#v", out.Funcs[0].Body.Stmts[0])
	}
	if _, ok := stmt.Value.(*ir.Call); !ok || stmt.ValueNodeID == 0 {
		t.Fatalf("expected call with source identity, got %#v", stmt)
	}
}

func TestGenerateHIRPreservesDiscardedOwnedCompositeIdentity(t *testing.T) {
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
		if _, dropped := exprStmt.Value.(*ir.Drop); dropped || exprStmt.ValueNodeID == 0 {
			t.Fatalf("statement %d = %#v, want raw expression with source identity", index, exprStmt)
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

func TestGenerateHIRPreservesOwnerBearingProjectionIdentity(t *testing.T) {
	out := generateTestHIR(t, "hir_projected_owner_temporary_test"+peeper.SourceExt, "hir_projected_owner_temporary_test", `struct Box { value: i32, ptr: *i32 }
fn make() -> Box;
fn read() -> i32 {
	return make().value;
}`)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	field, ok := ret.Value.(*ir.Field)
	if !ok || field.DropBase || field.NodeID == 0 {
		t.Fatalf("expected projection source identity without embedded cleanup, got %#v", ret.Value)
	}
}

func TestGenerateHIRKeepsOwnerBearingProjectionPlaceLive(t *testing.T) {
	out := generateTestHIR(t, "hir_projected_owner_place_test"+peeper.SourceExt, "hir_projected_owner_place_test", `struct Box { value: i32, ptr: *i32 }
fn read(box: Box) -> i32 {
	return box.value;
}`)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	load, ok := ret.Value.(*ir.Load)
	if !ok || load.DropRoot {
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
	if lit.Dynamic || out.Types.Text(lit.Type) != "[3]i32" || len(lit.Values) != 3 {
		t.Fatalf("unexpected array literal lowering: type=%q values=%d", out.Types.Text(lit.Type), len(lit.Values))
	}
}

func TestGenerateHIRLowersDynamicArrayLiteral(t *testing.T) {
	const filePath = "hir_dynamic_array_lit_test" + peeper.SourceExt
	src := `fn values() -> []i32 {
	return []i32{1, 2, 3};
}`
	out := generateTestHIR(t, filePath, "hir_dynamic_array_lit_test", src)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	lit, ok := ret.Value.(*ir.ArrayLit)
	if !ok || !lit.Dynamic || out.Types.Text(lit.Type) != "[]i32" || len(lit.Values) != 3 {
		t.Fatalf("unexpected dynamic array literal lowering: %#v", ret.Value)
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
	if view, ok := binding.Value.(*ir.SliceView); !ok || out.Types.Text(view.Type) != "&[]i32" {
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
	if view, ok := binding.Value.(*ir.SliceView); !ok || out.Types.Text(view.Type) != "&mut []i32" {
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
	if !ok || out.Types.Text(view.Type) != "&mut []i32" {
		t.Fatalf("expected nested mutable SliceView, got %#v", binding.Value)
	}
	if view.Source == nil || len(view.Source.Projections) != 1 || view.Source.Projections[0].Kind != ir.PlaceProjectionField {
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
	if address, ok := binding.Value.(*ir.AddrOf); !ok || out.Types.Text(address.Type) != "rawptr" {
		t.Fatalf("expected raw pointer AddrOf, got %#v", binding.Value)
	}
}

func TestGenerateHIRLowersMixedProjectionPlace(t *testing.T) {
	const src = `struct Token { value: i32 }
struct Bucket { items: []Token }
fn read(bucket: &Bucket, index: usize) -> i32 {
	return bucket.items[index].value;
}
fn write(bucket: &mut Bucket, index: usize, value: i32) {
	bucket.items[index].value = value;
}`
	out := generateTestHIR(t, "hir_mixed_projection_place_test"+peeper.SourceExt, "hir_mixed_projection_place_test", src)
	if len(out.Funcs) != 2 {
		t.Fatalf("functions = %d, want 2", len(out.Funcs))
	}
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	load, ok := ret.Value.(*ir.Load)
	if !ok || load.Place == nil {
		t.Fatalf("expected projected place load, got %#v", ret.Value)
	}
	want := []ir.PlaceProjectionKind{
		ir.PlaceProjectionDeref,
		ir.PlaceProjectionField,
		ir.PlaceProjectionIndex,
		ir.PlaceProjectionField,
	}
	if len(load.Place.Projections) != len(want) {
		t.Fatalf("read projections = %#v, want %v", load.Place.Projections, want)
	}
	for index, kind := range want {
		if load.Place.Projections[index].Kind != kind {
			t.Fatalf("read projection %d = %d, want %d", index, load.Place.Projections[index].Kind, kind)
		}
	}
	assign := out.Funcs[1].Body.Stmts[0].(*hir.Assign)
	if assign.Target == nil || len(assign.Target.Projections) != len(want) {
		t.Fatalf("write target = %#v, want mixed place", assign.Target)
	}
	for index, kind := range want {
		if assign.Target.Projections[index].Kind != kind {
			t.Fatalf("write projection %d = %d, want %d", index, assign.Target.Projections[index].Kind, kind)
		}
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
	if !ok || len(consumer.Slots) != 1 || consumer.Slots[0].SlotType == ir.InvalidType {
		t.Fatalf("expected interface carrier slot, got %#v", call.Args[0])
	}
}

func TestGenerateHIRPreservesTemporaryBorrow(t *testing.T) {
	const src = `struct Box { value: i32 }
fn Make() -> Box { return .{ value = 1 }; }
fn Read(_: &Box) -> i32 { return 0; }
fn main() -> i32 { return Read(&Make()); }`
	out := generateTestHIR(t, "hir_temporary_borrow_test"+peeper.SourceExt, "hir_temporary_borrow_test", src)
	var mainFn *hir.Function
	for _, fn := range out.Funcs {
		if fn.Name == "main" {
			mainFn = fn
			break
		}
	}
	if mainFn == nil || mainFn.Body == nil || len(mainFn.Body.Stmts) != 1 {
		t.Fatalf("unexpected main HIR: %#v", mainFn)
	}
	ret := mainFn.Body.Stmts[0].(*hir.Return)
	call, ok := ret.Value.(*ir.Call)
	if !ok || len(call.Args) != 1 {
		t.Fatalf("expected Read call, got %#v", ret.Value)
	}
	temporary, ok := call.Args[0].(*ir.TempBorrow)
	if !ok || temporary.Value == nil || out.Types.Text(temporary.Type) != "&struct{value: i32}" || temporary.Slice {
		t.Fatalf("expected temporary Box borrow, got %#v", call.Args[0])
	}
}

func TestUnusedOwnerCallBindingIsNotDiscarded(t *testing.T) {
	decl := &ast.LetDecl{Value: &ast.CallExpr{}}
	sym := symbols.New("owner", symbols.SymbolVar, decl, nil)
	sym.BindType(&typeinfo.OwnedPtrType{Target: &typeinfo.IntegerType{Signed: true, Bits: 32}})
	if shouldDiscardBindingValue(sym) {
		t.Fatalf("unused owner-returning call must remain materialized for cleanup")
	}
}

func TestGenerateHIRDiscardsUnusedCopyableCallBinding(t *testing.T) {
	out := generateTestHIR(t, "hir_discard_binding_test"+peeper.SourceExt, "hir_discard_binding_test", `fn make() -> i32;
fn main() { let ignored = make(); }`)
	if len(out.Funcs) != 1 || out.Funcs[0].Body == nil || len(out.Funcs[0].Body.Stmts) != 1 {
		t.Fatalf("unexpected HIR: %#v", out)
	}
	stmt, ok := out.Funcs[0].Body.Stmts[0].(*hir.ExprStmt)
	if !ok {
		t.Fatalf("unused copyable call = %T, want expression statement", out.Funcs[0].Body.Stmts[0])
	}
	if _, ok := stmt.Value.(*ir.Call); !ok {
		t.Fatalf("discarded value = %T, want call", stmt.Value)
	}
}
