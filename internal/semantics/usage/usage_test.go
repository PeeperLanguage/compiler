package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

func checkUsageSource(t *testing.T, src string, setupImports bool) *diagnostics.DiagnosticBag {
	t.Helper()
	const filePath = "usage_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)

	if setupImports {
		// Mock an external dependency module named "external"
		extSrc := `struct MyType {
	value: i32,
}
fn GetValue() -> i32 { return 42; }`
		extAST := parser.New("external"+peeper.SourceExt, lexer.New("external"+peeper.SourceExt, extSrc, diag).Tokenize(), diag).ParseModule()
		extMod := &project.Module{
			Key:        "local:external" + peeper.SourceExt,
			ImportPath: "external",
			FilePath:   "external" + peeper.SourceExt,
			Content:    extSrc,
			AST:        extAST,
			Imports:    make(map[string]project.ResolvedImport),
		}
		ctx.AddModule(extMod)
		collector.Collect(ctx, extMod)
		resolver.Resolve(ctx, extMod)
		typechecker.Check(ctx, extMod)
	}

	stream := lexer.New(filePath, src, diag).Tokenize()
	modAST := parser.New(filePath, stream, diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "usage_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]project.ResolvedImport),
	}

	if setupImports {
		module.Imports["external"] = project.ResolvedImport{
			Key:        "local:external" + peeper.SourceExt,
			ImportPath: "external",
			FilePath:   "external" + peeper.SourceExt,
			Origin:     project.ModuleOriginLocal,
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

func TestUnusedLocalIgnoredOnlyWithUnderscore(t *testing.T) {
	src := `fn main() -> i32 {
	let _: i32 = 0;
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if hasCode(diag, diagnostics.WarnUnusedLocal) {
		t.Fatalf("did not expect unused local warning for discard binding, got:\n%s", diag.EmitAllToString())
	}
}

func TestMultipleDiscardLocalsAllowed(t *testing.T) {
	src := `fn main() -> i32 {
	let _: i32 = 0;
	let _: i32 = 1;
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if diag.HasErrors() || hasCode(diag, diagnostics.WarnUnusedLocal) {
		t.Fatalf("expected multiple discard locals without errors or unused warnings, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnusedLocalWarnsWithUnderscorePrefix(t *testing.T) {
	src := `fn main() -> i32 {
	let _x: i32 = 0;
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if !hasCode(diag, diagnostics.WarnUnusedLocal) {
		t.Fatalf("expected unused local warning for underscore-prefixed binding, got:\n%s", diag.EmitAllToString())
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

func TestUnusedParameterIgnoredOnlyWithUnderscore(t *testing.T) {
	src := `fn main(_: i32) -> i32 {
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if hasCode(diag, diagnostics.WarnUnusedParameter) {
		t.Fatalf("did not expect unused parameter warning for discard binding, got:\n%s", diag.EmitAllToString())
	}
}

func TestMultipleDiscardParametersAllowed(t *testing.T) {
	src := `fn main(_: i32, _: i32) -> i32 {
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if diag.HasErrors() || hasCode(diag, diagnostics.WarnUnusedParameter) {
		t.Fatalf("expected multiple discard params without errors or unused warnings, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnusedParameterWarnsWithUnderscorePrefix(t *testing.T) {
	src := `fn main(_x: i32) -> i32 {
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if !hasCode(diag, diagnostics.WarnUnusedParameter) {
		t.Fatalf("expected unused parameter warning for underscore-prefixed binding, got:\n%s", diag.EmitAllToString())
	}
}

func TestUnusedReceiverParameterWarnsLikeAnyOtherParam(t *testing.T) {
	src := `impl i32 {
	fn to_str(value: Self) -> cstr {
		return "ok";
	}
}

fn main() -> i32 {
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	if !hasCode(diag, diagnostics.WarnUnusedParameter) {
		t.Fatalf("expected unused parameter warning for receiver param, got:\n%s", diag.EmitAllToString())
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
	let x: external::MyType;
	return 0;
}`
	diag := checkUsageSource(t, src, true)
	if hasCode(diag, diagnostics.WarnUnusedImport) {
		t.Fatalf("did not expect unused import warning when used in type, got:\n%s", diag.EmitAllToString())
	}
}

func TestUsageWarningsFixture(t *testing.T) {
	srcPath := filepath.Join("..", "..", "..", "x_test", "usage_warnings", peeper.SourceDirName, peeper.MainFileName)
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

	foundIgnoredLocal := false
	foundIgnoredParam := false
	for _, d := range diag.Diagnostics() {
		if d == nil {
			continue
		}
		if strings.Contains(d.Message, "_ignored_local") {
			foundIgnoredLocal = true
		}
		if strings.Contains(d.Message, "_ignored_param") {
			foundIgnoredParam = true
		}
		if strings.Contains(d.Message, "UnusedPublicFunction") {
			t.Errorf("did not expect warning on UnusedPublicFunction, got: %s", d.Message)
		}
		if strings.Contains(d.Message, "main") {
			t.Errorf("did not expect warning on main, got: %s", d.Message)
		}
	}
	if !foundIgnoredLocal {
		t.Errorf("expected warning on underscore-prefixed local")
	}
	if !foundIgnoredParam {
		t.Errorf("expected warning on underscore-prefixed parameter")
	}
}

func TestUnusedLocalHasLocation(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = 0;
	return 0;
}`
	diag := checkUsageSource(t, src, false)
	found := false
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.WarnUnusedLocal {
			found = true
			if len(item.Labels) == 0 {
				t.Fatalf("expected warning to have label / location info, got none")
			}
			loc := item.Labels[0].Location
			if loc == nil || loc.Start == nil || loc.Start.Line != 2 {
				t.Fatalf("expected warning label to point to line 2, got: %+v", loc.Start)
			}
		}
	}
	if !found {
		t.Fatalf("expected unused local warning")
	}
}
