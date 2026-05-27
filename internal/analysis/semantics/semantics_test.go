package semantics

import (
	"strings"
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/collector"
	"compiler/internal/analysis/semantics/resolver"
	"compiler/internal/analysis/semantics/typechecher"
	"compiler/internal/context"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

func testModule(src string) (*context.Module, *diagnostics.DiagnosticBag, bool) {
	diag := diagnostics.NewDiagnosticBag("test.em")
	ctx := context.NewWithConfig(context.Config{}, diag)
	stream := lexer.Lex("test.em", src, diag)
	mod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	if diag.HasErrors() || mod == nil {
		return module, diag, false
	}
	module.AST = mod
	if !collector.Collect(ctx, module, diag) || diag.HasErrors() || module.Decls == nil {
		return module, diag, false
	}
	if !resolver.Resolve(module, diag) || diag.HasErrors() || module.Bindings == nil {
		return module, diag, false
	}
	if !typechecher.Check(module, diag) || diag.HasErrors() || module.Types == nil {
		return module, diag, false
	}
	return module, diag, true
}

func TestCollectResolveTypecheckArithmeticMain(t *testing.T) {
	src := `fn helper(x: i32) -> i32 {
	return x;
}

fn main() -> i32 {
	let a = 10;
	let b = a + 2;
	return b;
}`
	module, diag, ok := testModule(src)
	if !ok {
		t.Fatalf("full semantics failed for %s:\n%s", module.FilePath, diag.EmitAllToString())
	}
}

func TestCollectorKeepsMultipleFunctions(t *testing.T) {
	src := `fn helper(x: i32) -> i32 {
	return x;
}

fn main() -> i32 {
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	ctx := context.NewWithConfig(context.Config{}, diag)
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	if !collector.Collect(ctx, module, diag) || module.Decls == nil {
		t.Fatalf("collect failed: %s", diag.EmitAllToString())
	}
	if len(module.Decls.Functions) != 2 {
		t.Fatalf("expected two collected functions, got %d", len(module.Decls.Functions))
	}
}

func TestResolveRejectsUnknownSymbol(t *testing.T) {
	src := `fn main() -> i32 {
	return missing;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	ctx := context.NewWithConfig(context.Config{}, diag)
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	if !collector.Collect(ctx, module, diag) {
		t.Fatalf("collect failed unexpectedly: %s", diag.EmitAllToString())
	}
	if resolver.Resolve(module, diag) {
		t.Fatalf("resolve should fail")
	}
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
}

func TestResolveTypecheckNestedBlockShadowing(t *testing.T) {
	src := `fn main() -> i32 {
	let a = 1;
	{
		let a = 2;
		return a;
	}
}`
	module, diag, ok := testModule(src)
	if !ok {
		t.Fatalf("nested block semantics failed for %s:\n%s", module.FilePath, diag.EmitAllToString())
	}
}

func TestResolveTypecheckIfElseReturns(t *testing.T) {
	src := `fn main() -> i32 {
	if 1 < 2 {
		return 1;
	} else if 2 < 3 {
		return 2;
	} else {
		return 3;
	}
}`
	module, diag, ok := testModule(src)
	if !ok {
		t.Fatalf("if/else semantics failed for %s:\n%s", module.FilePath, diag.EmitAllToString())
	}
}

func TestResolveTypecheckBuiltinScalars(t *testing.T) {
	src := `fn ints(a: i64, b: u64) -> u64 {
	const c: u64 = 5;
	return b + c;
}

fn floats(x: f32, y: f64) -> f64 {
	return y + 2.5;
}

fn main() -> i32 {
	return 0;
}`
	module, diag, ok := testModule(src)
	if !ok {
		t.Fatalf("builtin scalar semantics failed for %s:\n%s", module.FilePath, diag.EmitAllToString())
	}
}

func TestResolveSuggestsClosestSymbol(t *testing.T) {
	src := `fn main() -> i32 {
	const pi = 3;
	const pisa = 2;
	if 1 < pis {
		return 1;
	}
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	ctx := context.NewWithConfig(context.Config{}, diag)
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	if !collector.Collect(ctx, module, diag) {
		t.Fatalf("collect failed unexpectedly: %s", diag.EmitAllToString())
	}
	if resolver.Resolve(module, diag) {
		t.Fatalf("resolve should fail")
	}
	out := diag.EmitAllToString()
	if !diag.HasErrors() || !strings.Contains(out, "did you mean `pisa`?") {
		t.Fatalf("expected suggestion diagnostic, got:\n%s", out)
	}
}

func TestResolveReportsUseBeforeDecl(t *testing.T) {
	src := `fn main() -> i32 {
	return total;
	let total = 10;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	ctx := context.NewWithConfig(context.Config{}, diag)
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	if !collector.Collect(ctx, module, diag) {
		t.Fatalf("collect failed unexpectedly: %s", diag.EmitAllToString())
	}
	if resolver.Resolve(module, diag) {
		t.Fatalf("resolve should fail")
	}
	if !diag.HasErrors() {
		t.Fatalf("expected use-before-decl diagnostic")
	}
	found := false
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrUseBeforeDecl && len(item.Labels) >= 2 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected use-before-decl diagnostic with secondary label, got:\n%s", diag.EmitAllToString())
	}
}
