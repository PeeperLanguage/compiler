package analysis_test

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
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
	"compiler/internal/semantics/typeinfo"
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
	mod.Analysis = analysis.Run(diag, analysis.Input{Source: mod.THIR, CFG: mod.CFG, Scope: mod.ModuleScope, SymbolIndex: mod.SymbolIndex})
	return mod, diag
}

func TestRunPublishesVariantRefinementEvidence(t *testing.T) {
	mod, diag := checkAnalysisSource(t, `enum Choice {
	Left: { value: i32 },
	Right: { value: i32 },
	Pending,
}
fn read(choice: Choice) -> i32 {
	if choice is Choice::Left { return choice.value; }
	return 0;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := mod.AST.Stmts[1].(*ast.FnDecl)
	branch := fn.Body.Stmts[0].(*ast.IfStmt)
	test := branch.Cond.(*ast.IsExpr)
	field := branch.Then.Stmts[0].(*ast.ReturnStmt).Value.(*ast.SelectorExpr)

	caseTest, ok := mod.Analysis.CaseTest(test.ID())
	if !ok || caseTest.Case != 0 || caseTest.SubjectID != test.Value.ID() || caseTest.CaseCount != 3 {
		t.Fatalf("case-test evidence = %#v, %v", caseTest, ok)
	}
	payload, ok := mod.Analysis.Payload(field.ID())
	if !ok || len(payload.Cases) != 1 || payload.Cases[0] != 0 {
		t.Fatalf("payload evidence = %#v, %v", payload, ok)
	}
	access, ok := mod.Analysis.VariantField(field.ID())
	if !ok || access.Case != 0 || typeinfo.TypeText(access.Type) != "i32" {
		t.Fatalf("variant field evidence = %#v, %v", access, ok)
	}
	if got := typeinfo.TypeText(mod.EffectiveExprType(field.ID())); got != "i32" {
		t.Fatalf("effective field type = %s, want i32", got)
	}
	origins, ok := mod.Analysis.Origins(field.ID())
	if !ok || len(origins.Storage) == 0 {
		t.Fatalf("field origins = %#v, %v", origins, ok)
	}
}

func TestRunInvalidatesVariantProofAfterMutation(t *testing.T) {
	_, diag := checkAnalysisSource(t, `enum Choice { Left: { value: i32 }, Right: { value: i32 } }
fn read(mut choice: Choice) -> i32 {
	if choice is Choice::Left {
		choice = Choice::Right with .{ value = 0 };
		return choice.value;
	}
	return 0;
}`)
	if !diag.HasErrors() {
		t.Fatal("expected invalidated exact-case field diagnostic")
	}
}

func TestRunPreservesVariantFactsAcrossStableCFGPaths(t *testing.T) {
	_, diag := checkAnalysisSource(t, `enum Choice { Left: { value: i32 }, Right: { value: i32 } }
struct Holder { choice: Choice }
fn field(holder: Holder) -> i32 {
	if holder.choice is Choice::Left { return holder.choice.value; }
	return 0;
}
fn loop(choice: Choice) -> i32 {
	for choice is Choice::Left { return choice.value; }
	return 0;
}
fn join(choice: Choice, flag: bool) -> i32 {
	if flag {
		if !(choice is Choice::Left) { return 0; }
	} else {
		if !(choice is Choice::Left) { return 0; }
	}
	return choice.value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected projection/CFG diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestEffectiveExprTypeFallsBackToTHIR(t *testing.T) {
	mod, diag := checkAnalysisSource(t, `fn value() -> i32 { return 7; }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := mod.AST.Stmts[0].(*ast.FnDecl)
	literal := fn.Body.Stmts[0].(*ast.ReturnStmt).Value
	if refined := mod.Analysis.ExprType(literal.ID()); refined != nil {
		t.Fatalf("unrefined literal unexpectedly has analysis type %s", typeinfo.TypeText(refined))
	}
	base := mod.BaseExprType(literal.ID())
	if base == nil || mod.EffectiveExprType(literal.ID()) != base {
		t.Fatalf("effective type did not fall back to THIR base: base=%v effective=%v", base, mod.EffectiveExprType(literal.ID()))
	}
}
