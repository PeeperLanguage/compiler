package resolver

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/pkg/peeper"
)

func checkResolveSource(t *testing.T, src string) *diagnostics.DiagnosticBag {
	t.Helper()
	const filePath = "resolver_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	modAST := parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule()
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "resolver_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	Resolve(ctx, module)
	return diag
}

func TestUnresolvedIdentifierSuggestionPrefersNearestScope(t *testing.T) {
	src := `const for: i32 = 1;

fn main(foo: i32) -> i32 {
	return foa;
}`
	diag := checkResolveSource(t, src)
	out := diag.EmitAllToString()
	if !strings.Contains(out, "did you mean `foo`?") {
		t.Fatalf("expected nearest-scope suggestion, got:\n%s", out)
	}
	if strings.Contains(out, "did you mean `for`?") {
		t.Fatalf("unexpected outer-scope suggestion, got:\n%s", out)
	}
}
