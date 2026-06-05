package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/collector"
	"compiler/internal/analysis/semantics/resolver"
	"compiler/internal/analysis/semantics/typechecker"
	"compiler/internal/context"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

func checkUsageSource(t *testing.T, src string, setupImports bool) *diagnostics.DiagnosticBag {
	t.Helper()
	const filePath = "usage_test.em"
	diag := diagnostics.NewDiagnosticBag(filePath)
	diag.AddSourceContent(filePath, src)
	ctx := context.New(".", ".em", diag)

	if setupImports {
		// Mock an external dependency module named "external"
		extSrc := `type MyType i32;
fn GetValue() -> i32 { return 42; }`
		extAST := parser.ParseModule("external.em", lexer.Lex("external.em", extSrc, diag), diag)
		extMod := &context.Module{
			Key:        "local:external.em",
			ImportPath: "external",
			FilePath:   "external.em",
			Content:    extSrc,
			AST:        extAST,
			Imports:    make(map[string]context.ResolvedImport),
		}
		ctx.AddModule(extMod)
		collector.Collect(ctx, extMod)
		resolver.Resolve(ctx, extMod)
		typechecker.Check(ctx, extMod)
	}

	stream := lexer.Lex(filePath, src, diag)
	modAST := parser.ParseModule(filePath, stream, diag)
	module := &context.Module{
		Key:        context.ModuleKeyFor(context.ModuleOriginLocal, filePath),
		ImportPath: "usage_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]context.ResolvedImport),
	}

	if setupImports {
		module.Imports["external"] = context.ResolvedImport{
			Key:        "local:external.em",
			ImportPath: "external",
			FilePath:   "external.em",
			Origin:     context.ModuleOriginLocal,
		}
	}

	ctx.AddModule(module)
	collector.Collect(ctx, module)
	resolver.Resolve(ctx, module)
	typechecker.Check(ctx, module)
	Analyze(ctx, module)
	return diag
}

func hasCode(diag *diagnostics.DiagnosticBag, code string) bool {
	if diag == nil {
		return false
	}
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}

func TestUnusedLocal(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = 0;
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if !hasCode(diag, diagnostics.WarnUnusedLocal) {
		t.Fatalf("expected unused local warning, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnusedLocalIgnoredWithUnderscore(t *testing.T) {
	src := `fn main() -> i32 {
	let _x: i32 = 0;
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if hasCode(diag, diagnostics.WarnUnusedLocal) {
		t.Fatalf("did not expect unused local warning with underscore, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnusedParameter(t *testing.T) {
	src := `fn main(x: i32) -> i32 {
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if !hasCode(diag, diagnostics.WarnUnusedParameter) {
		t.Fatalf("expected unused parameter warning, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnusedParameterIgnoredWithUnderscore(t *testing.T) {
	src := `fn main(_x: i32) -> i32 {
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if hasCode(diag, diagnostics.WarnUnusedParameter) {
		t.Fatalf("did not expect unused parameter warning with underscore, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnusedPrivateFunction(t *testing.T) {
	src := `fn unused_func() -> i32 {
	return 42;
}
fn main() -> i32 {
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if !hasCode(diag, diagnostics.WarnUnusedPrivateFunction) {
		t.Fatalf("expected unused private function warning, got:\n%s", diag.EmitAllToString())
	}
}

func TestUsedPrivateFunction(t *testing.T) {
	src := `fn used_func() -> i32 {
	return 42;
}
fn main() -> i32 {
	return used_func();
}`
	diag := checkUsageSource(t, src, false)
	if hasCode(diag, diagnostics.WarnUnusedPrivateFunction) {
		t.Fatalf("did not expect unused private function warning, got:\n%s", diag.EmitAllToString())
	}
}

func TestPublicFunctionUnused(t *testing.T) {
	src := `fn UnusedPublic() -> i32 {
	return 10;
}
fn main() -> i32 {
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if hasCode(diag, diagnostics.WarnUnusedPrivateFunction) {
		t.Fatalf("did not expect unused function warning for public function, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnusedImport(t *testing.T) {
	src := `import "external";
fn main() -> i32 {
	return 0;
}`
	diag := checkUsageSource(t, src, true)
	if !hasCode(diag, diagnostics.WarnUnusedImport) {
		t.Fatalf("expected unused import warning, got:\n%s", diag.EmitAllToString())
	}
}

func TestUsedImportInScopeResolution(t *testing.T) {
	src := `import "external";
fn main() -> i32 {
	return external::GetValue();
}`
	diag := checkUsageSource(t, src, true)
	if hasCode(diag, diagnostics.WarnUnusedImport) {
		t.Fatalf("did not expect unused import warning, got:\n%s", diag.EmitAllToString())
	}
}

func TestUsedImportInType(t *testing.T) {
	src := `import "external";
fn main() -> i32 {
	let x: external::MyType = 12;
	return x;
}`
	diag := checkUsageSource(t, src, true)
	if hasCode(diag, diagnostics.WarnUnusedImport) {
		t.Fatalf("did not expect unused import warning when used in type, got:\n%s", diag.EmitAllToString())
	}
}

func TestUsageWarningsFixture(t *testing.T) {
	srcPath := filepath.Join("..", "..", "..", "..", "x_test", "usage_warnings_0.em")
	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	diag := checkUsageSource(t, string(src), true)

	// Assert expected warning codes:
	expected := []string{
		diagnostics.WarnUnusedImport,
		diagnostics.WarnUnusedPrivateType,
		diagnostics.WarnUnusedPrivateFunction,
		diagnostics.WarnUnusedLocal,
		diagnostics.WarnUnusedParameter,
	}
	for _, code := range expected {
		if !hasCode(diag, code) {
			t.Errorf("expected warning code %s, but got none. All diagnostics:\n%s", code, diag.EmitAllToString())
		}
	}

	// Assert that ignored / public / main symbols do not trigger warnings:
	for _, d := range diag.Diagnostics() {
		if d == nil {
			continue
		}
		if strings.Contains(d.Message, "_ignored_local") {
			t.Errorf("did not expect warning on _ignored_local, got: %s", d.Message)
		}
		if strings.Contains(d.Message, "_ignored_param") {
			t.Errorf("did not expect warning on _ignored_param, got: %s", d.Message)
		}
		if strings.Contains(d.Message, "UnusedPublicFunction") {
			t.Errorf("did not expect warning on UnusedPublicFunction, got: %s", d.Message)
		}
		if strings.Contains(d.Message, "main") {
			t.Errorf("did not expect warning on main, got: %s", d.Message)
		}
	}
}
