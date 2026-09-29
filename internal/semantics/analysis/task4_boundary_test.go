package analysis_test

import (
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir/cfg"
	"compiler/internal/semantics/analysis"
)

func TestRunChecksDefiniteInitializationAndPublishesCleanup(t *testing.T) {
	mod, diag := checkAnalysisSource(t, `fn make() -> *i32;
fn valid() { let owned = make(); }
fn bad() -> i32 { let mut value: i32; return value; }`)
	mod.Analysis = analysis.Run(diag, analysis.Input{
		Source: mod.THIR, CFG: mod.CFG, Scope: mod.ModuleScope, SymbolIndex: mod.SymbolIndex,
	})
	if !strings.Contains(diag.EmitAllToString(), "used before it's initialized") {
		t.Fatalf("missing definite-initialization diagnostic:\n%s", diag.EmitAllToString())
	}
	valid := mod.AST.Stmts[1].(*ast.FnDecl)
	fn := mod.THIR.Function(valid.ID())
	graph := mod.CFG.FunctionByID(fn.Identity)
	var exit cfg.SiteID
	for _, block := range graph.Blocks {
		for _, site := range block.Sites {
			if site != nil && site.Kind == cfg.SiteScopeExit && site.NodeID == valid.Body.ID() {
				exit = site.ID
			}
		}
	}
	if got := mod.Analysis.DropsAfterSite(fn.Identity, exit); len(got) != 1 {
		t.Fatalf("scope cleanup = %v, want one owned binding", got)
	}
}

func TestCleanupQueriesAreFunctionScoped(t *testing.T) {
	mod, diag := checkAnalysisSource(t, `fn make() -> *i32;
fn first() { let one = make(); }
fn second() { let two = make(); }`)
	mod.Analysis = analysis.Run(diag, analysis.Input{
		Source: mod.THIR, CFG: mod.CFG, Scope: mod.ModuleScope, SymbolIndex: mod.SymbolIndex,
	})
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	first := mod.AST.Stmts[1].(*ast.FnDecl)
	second := mod.AST.Stmts[2].(*ast.FnDecl)
	firstFn := mod.THIR.Function(first.ID())
	secondFn := mod.THIR.Function(second.ID())
	findExit := func(fn *ast.FnDecl) cfg.SiteID {
		graph := mod.CFG.FunctionByID(mod.THIR.Function(fn.ID()).Identity)
		for _, block := range graph.Blocks {
			for _, site := range block.Sites {
				if site != nil && site.Kind == cfg.SiteScopeExit && site.NodeID == fn.Body.ID() {
					return site.ID
				}
			}
		}
		return cfg.SiteID{}
	}
	firstDrops := mod.Analysis.DropsAfterSite(firstFn.Identity, findExit(first))
	secondDrops := mod.Analysis.DropsAfterSite(secondFn.Identity, findExit(second))
	if len(firstDrops) != 1 || len(secondDrops) != 1 || firstDrops[0] == secondDrops[0] {
		t.Fatalf("function-scoped cleanup leaked: first=%v second=%v", firstDrops, secondDrops)
	}
}
