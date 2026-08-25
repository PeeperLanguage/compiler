package lower

import (
	"fmt"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/hir"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typechecker"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

func generateTestHIR(t *testing.T, filePath, importPath, src string, beforeLower ...func(*project.Module)) *hir.Module {
	t.Helper()
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: importPath,
		FilePath:   filePath,
		IsEntry:    true,
		Content:    src,
		AST:        parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	typechecker.Check(ctx, module)
	module.TypedASTNodes = ast.Index(module.AST)
	module.CFG = cfg.BuildModule(module.AST)
	module.Flow = typechecker.CheckFlow(ctx, module)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	for _, prepare := range beforeLower {
		prepare(module)
	}
	out := GenerateHIR(ctx, module)
	return out
}

func TestGenerateHIRCallableNamesAreStableAndModuleAware(t *testing.T) {
	const src = `struct Counter { value: i32 }
fn Value() -> i32 { return 1; }
fn (self: Counter) Read() -> i32 { return self.value; }`
	first := generateTestHIR(t, "first"+peeper.SourceExt, "sample/first", src)
	repeated := generateTestHIR(t, "first"+peeper.SourceExt, "sample/first", src)
	second := generateTestHIR(t, "second"+peeper.SourceExt, "sample/second", src)
	if len(first.Funcs) != 2 || len(repeated.Funcs) != 2 || len(second.Funcs) != 2 {
		t.Fatalf("unexpected function counts: %d, %d, %d", len(first.Funcs), len(repeated.Funcs), len(second.Funcs))
	}
	for index := range first.Funcs {
		if first.Funcs[index].Name != repeated.Funcs[index].Name {
			t.Fatalf("callable name is not deterministic: %q != %q", first.Funcs[index].Name, repeated.Funcs[index].Name)
		}
		if first.Funcs[index].Name == second.Funcs[index].Name {
			t.Fatalf("module callables collide at index %d: %q", index, first.Funcs[index].Name)
		}
		if strings.Contains(first.Funcs[index].Name, "$") {
			t.Fatalf("callable linker name uses local instance delimiter: %q", first.Funcs[index].Name)
		}
	}
}

func TestCallableNameFramesModuleIdentityComponents(t *testing.T) {
	first := symbols.New("Value", symbols.SymbolFunc, nil, nil)
	first.DefiningModule = symbols.DefiningModuleKey{Origin: "local", Namespace: "ab", Dependency: "c", ImportPath: "sample/value"}
	second := symbols.New("Value", symbols.SymbolFunc, nil, nil)
	second.DefiningModule = symbols.DefiningModuleKey{Origin: "local", Namespace: "a", Dependency: "bc", ImportPath: "sample/value"}
	firstName, _ := callableName(nil, first)
	secondName, _ := callableName(nil, second)
	if firstName == secondName {
		t.Fatalf("length-ambiguous module identities collide: %q", firstName)
	}
}

func TestSymbolNameLeavesCompilerOwnedFunctionUnmangled(t *testing.T) {
	sym := symbols.New("alloc", symbols.SymbolFunc, nil, nil)
	sym.CompilerOp = symbols.CompilerOpAlloc
	want := fmt.Sprintf("alloc$%d", sym.ID)
	if got := symbolName(nil, sym); got != want {
		t.Fatalf("compiler-owned symbol name = %q, want %q", got, want)
	}
}

func TestGenerateHIRPreservesExternLinkName(t *testing.T) {
	out := generateTestHIR(t, "extern_name"+peeper.SourceExt, "sample/extern", `#[extern("native_ping")]
fn ping() -> i32;`)
	if len(out.Externs) != 1 || out.Externs[0].Name != "native_ping" {
		t.Fatalf("extern name = %#v, want native_ping", out.Externs)
	}
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

func TestGenerateHIRLowersOptionalFlowEvidence(t *testing.T) {
	out := generateTestHIR(t, "hir_optional_flow_test"+peeper.SourceExt, "hir_optional_flow_test", `fn read(value: ?i32) -> i32 {
	if value != none {
		return value;
	}
	return 0;
}`)
	branch, ok := out.Funcs[0].Body.Stmts[0].(*hir.If)
	if !ok {
		t.Fatalf("first statement = %T, want If", out.Funcs[0].Body.Stmts[0])
	}
	present, ok := branch.Cond.(*ir.OptionalPresent)
	if !ok || out.Types.Text(present.Value.TypeID()) != "?i32" || out.Types.Text(present.TypeID()) != "bool" {
		t.Fatalf("condition = %#v, want OptionalPresent(?i32) -> bool", branch.Cond)
	}
	ret, ok := branch.Then.Stmts[0].(*hir.Return)
	if !ok {
		t.Fatalf("then statement = %T, want Return", branch.Then.Stmts[0])
	}
	load, ok := ret.Value.(*ir.Load)
	if !ok || load.Place == nil || len(load.Place.Projections) != 1 ||
		load.Place.Projections[0].Kind != ir.PlaceProjectionOptionalPayload || out.Types.Text(load.TypeID()) != "i32" {
		t.Fatalf("proven value = %#v, want i32 optional payload load", ret.Value)
	}
}

func TestGenerateHIRKeepsOptionalIndexCarrierBeforePayloadProjection(t *testing.T) {
	out := generateTestHIR(t, "hir_optional_index_flow_test"+peeper.SourceExt, "hir_optional_index_flow_test", `fn read(values: [1]?i32) -> i32 {
	if values[0] == none {
		return 0;
	}
	return values[0];
}`)
	ret, ok := out.Funcs[0].Body.Stmts[1].(*hir.Return)
	if !ok {
		t.Fatalf("second statement = %T, want Return", out.Funcs[0].Body.Stmts[1])
	}
	load, ok := ret.Value.(*ir.Load)
	if !ok || load.Place == nil || len(load.Place.Projections) != 2 {
		t.Fatalf("proven index = %#v, want index then optional payload load", ret.Value)
	}
	index := load.Place.Projections[0]
	payload := load.Place.Projections[1]
	if index.Kind != ir.PlaceProjectionIndex || out.Types.Text(index.Type) != "?i32" ||
		payload.Kind != ir.PlaceProjectionOptionalPayload || out.Types.Text(payload.Type) != "i32" {
		t.Fatalf("projections = %#v, want index:?i32 then optional-payload:i32", load.Place.Projections)
	}
}

func TestGenerateHIRPreservesExplicitOptionalCarrierInsideProof(t *testing.T) {
	out := generateTestHIR(t, "hir_optional_carrier_test"+peeper.SourceExt, "hir_optional_carrier_test", `fn keep(value: ?i32) -> ?i32 {
	if value != none {
		let carrier: ?i32 = value;
		return carrier;
	}
	return none;
}`)
	branch, ok := out.Funcs[0].Body.Stmts[0].(*hir.If)
	if !ok || branch.Then == nil || len(branch.Then.Stmts) == 0 {
		t.Fatalf("first statement = %#v, want populated If", out.Funcs[0].Body.Stmts[0])
	}
	binding, ok := branch.Then.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("then statement = %T, want Binding", branch.Then.Stmts[0])
	}
	ident, ok := binding.Value.(*ir.Ident)
	if !ok || out.Types.Text(ident.TypeID()) != "?i32" {
		t.Fatalf("explicit carrier value = %#v, want ?i32 Ident", binding.Value)
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

func TestGenerateHIRRepresentsUninitializedBindingWithoutInvalidExpr(t *testing.T) {
	out := generateTestHIR(t, "hir_uninitialized_binding_test"+peeper.SourceExt, "hir_uninitialized_binding_test", `fn main() -> i32 {
	let mut value: i32;
	value = 7;
	return value;
}`)
	binding, ok := out.Funcs[0].Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("first statement = %#v, want binding", out.Funcs[0].Body.Stmts[0])
	}
	if binding.Value != nil {
		t.Fatalf("uninitialized binding value = %#v, want nil", binding.Value)
	}
	if got := out.Types.Text(binding.Type); got != "i32" {
		t.Fatalf("uninitialized binding type = %q, want i32", got)
	}
}

func TestGenerateHIRLowersDynamicArrayOwnerOperations(t *testing.T) {
	const src = `fn main() {
	let mut values = []i32{};
	values |> append(1);
	values |> reserve(8);
	values |> resize(4, 0);
	values |> shrink(2);
}`
	out := generateTestHIR(t, "hir_dynamic_array_ops_test"+peeper.SourceExt, "hir_dynamic_array_ops_test", src)
	want := []symbols.CompilerOp{symbols.CompilerOpAppend, symbols.CompilerOpReserve, symbols.CompilerOpResize, symbols.CompilerOpShrink}
	if len(out.Funcs[0].Body.Stmts) < len(want) {
		t.Fatalf("operation statements = %d, want at least %d", len(out.Funcs[0].Body.Stmts), len(want))
	}
	for i := range want {
		stmt := out.Funcs[0].Body.Stmts[i+1]
		exprStmt, ok := stmt.(*hir.ExprStmt)
		if !ok {
			t.Fatalf("stmt %d = %#v, want expression statement", i, stmt)
		}
		op, ok := exprStmt.Value.(*ir.DynamicArrayOp)
		if !ok || op.Op != want[i] || out.Types.Text(op.Type) != "void" || out.Types.Text(op.ArrayType) != "[]i32" {
			t.Fatalf("stmt %d operation = %#v, want %s mutable []i32", i, exprStmt.Value, want[i])
		}
		if _, ok := op.Array.(*ir.AddrOf); !ok {
			t.Fatalf("stmt %d owner = %#v, want semantic mutable borrow", i, op.Array)
		}
	}
}

func TestGenerateHIRLowersSliceViewIndexExpr(t *testing.T) {
	const filePath = "hir_slice_view_index_test" + peeper.SourceExt
	src := `fn first(xs: &[..]i32, index: usize) -> i32 {
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
	if !ok || out.Types.Text(view.Type) != "&mut [..]i32" || view.EndExclusive {
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
	let chars = text |> as_chars();
	return chars |> len() as i32;
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
	let byte = (Make() |> as_bytes())[0];
	let size = Make()[0..1] |> len();
	return byte as i32 + size as i32;
}`
	out := generateTestHIR(t, "hir_temporary_string_views_test"+peeper.SourceExt, "hir_temporary_string_views_test", src)
	mainFn := out.Funcs[1]
	byteBinding := mainFn.Body.Stmts[0].(*hir.Binding)
	byteLoad := byteBinding.Value.(*ir.Load)
	byteView := byteLoad.Place.Root.(*ir.SliceView)
	byteOwner, ok := byteView.Source.Root.(*ir.TempBorrow)
	if !ok {
		t.Fatalf("temporary as_bytes source = %T, want TempBorrow", byteView.Source.Root)
	}
	if !byteOwner.Slice {
		t.Fatal("temporary as_bytes borrow must use return-safe string view")
	}
	if _, ok := byteOwner.Value.(*ir.Call); !ok {
		t.Fatalf("temporary as_bytes value = %T, want one lowered call", byteOwner.Value)
	}

	sizeBinding := mainFn.Body.Stmts[1].(*hir.Binding)
	rangeLen := sizeBinding.Value.(*ir.Len)
	rangeView := rangeLen.Value.(*ir.SliceView)
	rangeOwner, ok := rangeView.Source.Root.(*ir.TempBorrow)
	if !ok {
		t.Fatalf("temporary string range source = %T, want TempBorrow", rangeView.Source.Root)
	}
	if !rangeOwner.Slice {
		t.Fatal("temporary string range borrow must use return-safe string view")
	}
}

func TestGenerateHIRLowersStringBorrowsAsSliceViews(t *testing.T) {
	const src = `fn borrow(text: str) {
	let view = &text;
	let _ = view |> len();
}`
	out := generateTestHIR(t, "hir_string_borrow_test"+peeper.SourceExt, "hir_string_borrow_test", src)
	binding, ok := out.Funcs[0].Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected string borrow binding, got %#v", out.Funcs[0].Body.Stmts[0])
	}
	view, ok := binding.Value.(*ir.SliceView)
	if !ok || out.Types.Text(view.Type) != "&str" {
		t.Fatalf("expected &str SliceView, got %#v", binding.Value)
	}
	if view.Source == nil || out.Types.Text(view.Source.Type) != "str" {
		t.Fatalf("expected str source place, got %#v", view.Source)
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

func TestGenerateHIRPreservesMixedShiftCountType(t *testing.T) {
	out := generateTestHIR(t, "hir_mixed_shift_test"+peeper.SourceExt, "hir_mixed_shift_test", `fn shift(value: u8, count: u16) -> u8 {
	return value << count;
}`)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	binary, ok := ret.Value.(*ir.Binary)
	if !ok || binary.Op != "<<" || out.Types.Text(binary.TypeID()) != "u8" {
		t.Fatalf("shift return = %#v, want u8 binary shift", ret.Value)
	}
	if out.Types.Text(binary.Right.TypeID()) != "u16" {
		t.Fatalf("shift count type = %s, want preserved u16", out.Types.Text(binary.Right.TypeID()))
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

func TestGenerateHIRLowersDynamicArrayBorrowsAsOwnerReferences(t *testing.T) {
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
	if len(out.Funcs) != 3 {
		t.Fatalf("unexpected function count: %d", len(out.Funcs))
	}

	explicit := out.Funcs[0]
	if explicit == nil || explicit.Body == nil || len(explicit.Body.Stmts) < 1 {
		t.Fatalf("unexpected explicit borrow HIR: %#v", explicit)
	}
	binding, ok := explicit.Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected explicit borrow binding, got %#v", explicit.Body.Stmts[0])
	}
	if ref, ok := binding.Value.(*ir.AddrOf); !ok || out.Types.Text(ref.Type) != "&[]i32" {
		t.Fatalf("expected shared owner reference, got %#v", binding.Value)
	}

	explicitMutable := out.Funcs[1]
	if explicitMutable == nil || explicitMutable.Body == nil || len(explicitMutable.Body.Stmts) < 1 {
		t.Fatalf("unexpected explicit mutable borrow HIR: %#v", explicitMutable)
	}
	binding, ok = explicitMutable.Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected explicit mutable borrow binding, got %#v", explicitMutable.Body.Stmts[0])
	}
	if ref, ok := binding.Value.(*ir.AddrOf); !ok || out.Types.Text(ref.Type) != "&mut []i32" {
		t.Fatalf("expected mutable owner reference, got %#v", binding.Value)
	}

	nested := out.Funcs[2]
	if nested == nil || nested.Body == nil || len(nested.Body.Stmts) < 1 {
		t.Fatalf("unexpected nested mutable borrow HIR: %#v", nested)
	}
	binding, ok = nested.Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected nested borrow binding, got %#v", nested.Body.Stmts[0])
	}
	ref, ok := binding.Value.(*ir.AddrOf)
	if !ok || out.Types.Text(ref.Type) != "&mut []i32" {
		t.Fatalf("expected nested mutable owner reference, got %#v", binding.Value)
	}
	if ref.Place == nil || len(ref.Place.Projections) != 1 || ref.Place.Projections[0].Kind != ir.PlaceProjectionField {
		t.Fatalf("expected nested reference to original field place, got %#v", ref.Place)
	}
}

func TestGenerateHIRLowersAddressAsOpaqueRawPointer(t *testing.T) {
	const filePath = "hir_raw_pointer_address_test" + peeper.SourceExt
	const src = `fn explicit(value: i32) {
	let _ = @value;
}`
	out := generateTestHIR(t, filePath, "hir_raw_pointer_address_test", src)
	if len(out.Funcs) != 1 {
		t.Fatalf("unexpected function count: %d", len(out.Funcs))
	}

	explicit := out.Funcs[0]
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
	if len(out.Funcs) != 2 {
		t.Fatalf("unexpected function count: %d", len(out.Funcs))
	}
	consume := out.Funcs[1]
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

func TestGenerateHIRConsumesRecordedInterfaceImplementations(t *testing.T) {
	const src = `iface Reader { fn (&Self) read() -> i32 }
struct Counter { value: i32 }
fn (self: &Counter) read() -> i32 { return self.value; }
fn main() -> i32 {
	let counter: Counter = .{ value = 7 };
	let reader: &Reader = &counter;
	return reader.read();
}`
	out := generateTestHIR(t, "hir_interface_evidence_test"+peeper.SourceExt, "hir_interface_evidence_test", src,
		func(module *project.Module) { module.Semantics.MethodSets = nil })
	mainFn := out.Funcs[len(out.Funcs)-1]
	binding, ok := mainFn.Body.Stmts[1].(*hir.Binding)
	if !ok {
		t.Fatalf("interface binding = %#v, want binding", mainFn.Body.Stmts[1])
	}
	carrier, ok := binding.Value.(*ir.InterfaceMake)
	if !ok || len(carrier.Slots) != 1 || carrier.Slots[0].MethodName != "read" {
		t.Fatalf("interface carrier = %#v, want recorded read slot", binding.Value)
	}
	if ir.StripSymbolInstance(carrier.Slots[0].FuncName) != out.Funcs[0].Name {
		t.Fatalf("interface slot target %q does not name method definition %q", carrier.Slots[0].FuncName, out.Funcs[0].Name)
	}
}

func TestGenerateHIRConsumesDistinctDefaultInterfaceEvidence(t *testing.T) {
	const src = `iface ReadA { fn (&Self) read_a() -> i32 }
iface ReadB { fn (&Self) read_b() -> i32 }
struct Counter { value: i32 }
fn (self: &Counter) read_a() -> i32 { return self.value; }
fn (self: &Counter) read_b() -> i32 { return self.value + 1; }
fn use(value: &Counter, first: &ReadA = value, second: &ReadB = value) -> i32 {
	return first.read_a() + second.read_b();
}
fn main() -> i32 {
	let counter: Counter = .{ value = 20 };
	return use(&counter);
}`
	out := generateTestHIR(t, "hir_default_interface_evidence_test"+peeper.SourceExt, "hir_default_interface_evidence_test", src,
		func(module *project.Module) { module.Semantics.MethodSets = nil })
	mainFn := out.Funcs[len(out.Funcs)-1]
	ret := mainFn.Body.Stmts[1].(*hir.Return)
	call := ret.Value.(*ir.Call)
	first, firstOK := call.Args[1].(*ir.InterfaceMake)
	second, secondOK := call.Args[2].(*ir.InterfaceMake)
	if !firstOK || !secondOK || len(first.Slots) != 1 || len(second.Slots) != 1 ||
		first.Slots[0].MethodName != "read_a" || second.Slots[0].MethodName != "read_b" {
		t.Fatalf("default interface carriers = %#v %#v", call.Args[1], call.Args[2])
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

func TestGenerateHIRConsumesPipeBorrowEvidence(t *testing.T) {
	out := generateTestHIR(t, "hir_pipe_borrow_test"+peeper.SourceExt, "hir_pipe_borrow_test", `fn Read(_: &i32) -> i32 { return 1; }
fn main() -> i32 {
	let value = 7;
	return value |> Read();
}`)
	mainFn := out.Funcs[len(out.Funcs)-1]
	ret := mainFn.Body.Stmts[1].(*hir.Return)
	call, ok := ret.Value.(*ir.Call)
	if !ok || len(call.Args) != 1 {
		t.Fatalf("pipe call = %#v, want one-argument call", ret.Value)
	}
	borrow, ok := call.Args[0].(*ir.AddrOf)
	if !ok || borrow.Place == nil || out.Types.Text(borrow.Type) != "&i32" {
		t.Fatalf("pipe argument = %#v, want semantic shared borrow", call.Args[0])
	}
}

func TestGenerateHIRConsumesMethodBorrowEvidence(t *testing.T) {
	out := generateTestHIR(t, "hir_method_borrow_evidence_test"+peeper.SourceExt, "hir_method_borrow_evidence_test", `struct Counter { value: i32 }
fn (self: &Counter) Read() -> i32 { return self.value; }
fn main() -> i32 {
	let counter: Counter = .{ value = 7 };
	return counter.Read();
}`, func(module *project.Module) { module.Semantics.MethodSets = nil })
	mainFn := out.Funcs[len(out.Funcs)-1]
	ret := mainFn.Body.Stmts[1].(*hir.Return)
	call, ok := ret.Value.(*ir.Call)
	if !ok || len(call.Args) != 1 {
		t.Fatalf("method call = %#v, want receiver argument", ret.Value)
	}
	borrow, ok := call.Args[0].(*ir.AddrOf)
	if !ok || borrow.Place == nil || out.Types.Text(borrow.Type) != "&struct{value: i32}" {
		t.Fatalf("method receiver = %#v, want semantic shared borrow", call.Args[0])
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
