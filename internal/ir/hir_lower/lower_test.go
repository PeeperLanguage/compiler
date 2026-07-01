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

func TestGenerateHIRLowersIndexExpr(t *testing.T) {
	const filePath = "hir_index_test" + peeper.SourceExt
	src := `fn first(xs: [4]i32) -> i32 {
	return xs[0];
}`
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "hir_index_test",
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
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}

	out := GenerateHIR(ctx, module)
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

func TestGenerateHIRLowersArrayLiteral(t *testing.T) {
	const filePath = "hir_array_lit_test" + peeper.SourceExt
	src := `fn first() -> [3]i32 {
	return [_]i32{1, 2, 3};
}`
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "hir_array_lit_test",
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
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}

	out := GenerateHIR(ctx, module)
	ret := out.Funcs[0].Body.Stmts[0].(*hir.Return)
	lit, ok := ret.Value.(*ir.ArrayLit)
	if !ok {
		t.Fatalf("expected array literal, got %#v", ret.Value)
	}
	if lit.TypeText() != "[3]i32" || len(lit.Values) != 3 {
		t.Fatalf("unexpected array literal lowering: type=%q values=%d", lit.TypeText(), len(lit.Values))
	}
}
