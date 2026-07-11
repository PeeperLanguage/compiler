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

impl []i32 {
	fn read(self: &Self) -> i32 {
		return 0;
	}

	fn write(self: &mut Self) {
	}
}

fn explicit(xs: []i32) {
	let _ = &xs;
}

fn explicit_mutable(mut xs: []i32) {
	let _ = &mut xs;
}

fn shared(xs: []i32) -> i32 {
	return xs.read();
}

fn mutable(mut xs: []i32) {
	xs.write();
}

fn nested(mut bucket: Bucket) {
	bucket.items.write();
}`
	out := generateTestHIR(t, filePath, "hir_slice_view_test", src)
	funcs := make(map[string]*hir.Function, len(out.Funcs))
	for _, fn := range out.Funcs {
		funcs[fn.Name] = fn
	}

	explicit := funcs["explicit"]
	if explicit == nil || explicit.Body == nil || len(explicit.Body.Stmts) != 1 {
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
	if explicitMutable == nil || explicitMutable.Body == nil || len(explicitMutable.Body.Stmts) != 1 {
		t.Fatalf("unexpected explicit mutable borrow HIR: %#v", explicitMutable)
	}
	binding, ok = explicitMutable.Body.Stmts[0].(*hir.Binding)
	if !ok {
		t.Fatalf("expected explicit mutable borrow binding, got %#v", explicitMutable.Body.Stmts[0])
	}
	if view, ok := binding.Value.(*ir.SliceView); !ok || view.TypeText() != "&mut []i32" {
		t.Fatalf("expected mutable SliceView, got %#v", binding.Value)
	}

	shared := funcs["shared"]
	if shared == nil || shared.Body == nil || len(shared.Body.Stmts) != 1 {
		t.Fatalf("unexpected shared receiver HIR: %#v", shared)
	}
	ret, ok := shared.Body.Stmts[0].(*hir.Return)
	if !ok {
		t.Fatalf("expected shared method return, got %#v", shared.Body.Stmts[0])
	}
	call, ok := ret.Value.(*ir.Call)
	if !ok || len(call.Args) == 0 {
		t.Fatalf("expected shared method call, got %#v", ret.Value)
	}
	if view, ok := call.Args[0].(*ir.SliceView); !ok || view.TypeText() != "&[]i32" {
		t.Fatalf("expected shared receiver SliceView, got %#v", call.Args[0])
	}

	mutable := funcs["mutable"]
	if mutable == nil || mutable.Body == nil || len(mutable.Body.Stmts) != 1 {
		t.Fatalf("unexpected mutable receiver HIR: %#v", mutable)
	}
	stmt, ok := mutable.Body.Stmts[0].(*hir.ExprStmt)
	if !ok {
		t.Fatalf("expected mutable method call statement, got %#v", mutable.Body.Stmts[0])
	}
	call, ok = stmt.Value.(*ir.Call)
	if !ok || len(call.Args) == 0 {
		t.Fatalf("expected mutable method call, got %#v", stmt.Value)
	}
	if view, ok := call.Args[0].(*ir.SliceView); !ok || view.TypeText() != "&mut []i32" {
		t.Fatalf("expected mutable receiver SliceView, got %#v", call.Args[0])
	}

	nested := funcs["nested"]
	if nested == nil || nested.Body == nil || len(nested.Body.Stmts) != 1 {
		t.Fatalf("unexpected nested receiver HIR: %#v", nested)
	}
	stmt, ok = nested.Body.Stmts[0].(*hir.ExprStmt)
	if !ok {
		t.Fatalf("expected nested method call statement, got %#v", nested.Body.Stmts[0])
	}
	call, ok = stmt.Value.(*ir.Call)
	if !ok || len(call.Args) == 0 {
		t.Fatalf("expected nested method call, got %#v", stmt.Value)
	}
	view, ok := call.Args[0].(*ir.SliceView)
	if !ok || view.TypeText() != "&mut []i32" {
		t.Fatalf("expected nested receiver SliceView, got %#v", call.Args[0])
	}
	field, ok := view.Source.(*ir.Field)
	if !ok || !field.ThroughPtr {
		t.Fatalf("expected nested view from original field place, got %#v", view.Source)
	}
}

func TestGenerateHIRKeepsRawPointerAddressContextSeparate(t *testing.T) {
	const filePath = "hir_raw_pointer_address_test" + peeper.SourceExt
	const src = `struct Counter {
	value: i32
}

impl Counter {
	fn read(self: *Self) -> i32 {
		return self.value;
	}
}

fn explicit(value: i32) {
	let _ = @value;
}

fn receiver(mut counter: Counter) -> i32 {
	return counter.read();
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
	if address, ok := binding.Value.(*ir.AddrOf); !ok || address.TypeText() != "*i32" {
		t.Fatalf("expected raw pointer AddrOf, got %#v", binding.Value)
	}

	receiver := funcs["receiver"]
	if receiver == nil || receiver.Body == nil || len(receiver.Body.Stmts) != 1 {
		t.Fatalf("unexpected raw pointer receiver HIR: %#v", receiver)
	}
	ret, ok := receiver.Body.Stmts[0].(*hir.Return)
	if !ok {
		t.Fatalf("expected raw pointer receiver return, got %#v", receiver.Body.Stmts[0])
	}
	call, ok := ret.Value.(*ir.Call)
	if !ok || len(call.Args) == 0 {
		t.Fatalf("expected raw pointer receiver call, got %#v", ret.Value)
	}
	if address, ok := call.Args[0].(*ir.AddrOf); !ok || address.TypeText() != "*struct{value: i32}" {
		t.Fatalf("expected raw pointer receiver AddrOf, got %#v", call.Args[0])
	}
}
