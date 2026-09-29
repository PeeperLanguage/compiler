package analysis_test

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir/cfg"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/analysis"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

func checkAnalysisSource(t *testing.T, src string) (*module.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "flow_analysis_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	mod := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "flow_analysis_test"},
		FilePath: filePath,
		Content:  src,
		AST:      parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(mod)
	collector.Collect(ctx, mod)
	binder.Bind(ctx, mod)
	resolver.Resolve(ctx, mod)
	mod.THIR = typechecker.Check(ctx, mod)
	mod.CFG = cfg.BuildModule(mod.THIR)
	mod.Analysis = analysis.Run(diag, analysis.Input{Source: mod.THIR, CFG: mod.CFG, Scope: mod.ModuleScope})
	return mod, diag
}

func TestRunPublishesFlowAnalysis(t *testing.T) {
	mod, diag := checkAnalysisSource(t, `fn main() { let value: ?i32 = 1; if value != none { let unwrapped: i32 = value; } }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if mod.Analysis == nil {
		t.Fatal("analysis was not published")
	}
}
