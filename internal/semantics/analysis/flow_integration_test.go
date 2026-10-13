package analysis_test

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/thir"
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
	Left: struct { value: i32 },
	Right: struct { value: i32 },
	Pending,
}
fn read(choice: Choice) -> i32 {
	if !(choice is Choice::Left) { return 0; }
	return choice.value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := mod.AST.Stmts[1].(*ast.FnDecl)
	branch := fn.Body.Stmts[0].(*ast.IfStmt)
	test := branch.Cond.(*ast.UnaryExpr).Expr.(*ast.IsExpr)
	// After the guard leaves on every other case, choice reads as the payload.
	field := fn.Body.Stmts[1].(*ast.ReturnStmt).Value.(*ast.SelectorExpr)

	tested, _ := mod.THIR.Node(test.ID()).(thir.Expr)
	caseTest := thir.CaseTestOf(tested)
	if caseTest == nil || caseTest.Case != 0 || caseTest.SubjectID != test.Value.ID() || caseTest.CaseCount != 3 {
		t.Fatalf("case-test evidence = %#v", caseTest)
	}
	payload, ok := mod.Analysis.Payload(field.Expr.ID())
	if !ok || len(payload.Cases) != 1 || payload.Cases[0] != 0 {
		t.Fatalf("payload evidence = %#v, %v", payload, ok)
	}
	if got := typeinfo.TypeText(mod.BaseExprType(field.ID())); got != "i32" {
		t.Fatalf("effective field type = %s, want i32", got)
	}
	origins, ok := mod.Analysis.Origins(field.ID())
	if !ok || len(origins.Storage) == 0 {
		t.Fatalf("field origins = %#v, %v", origins, ok)
	}
}

func TestRunPublishesCaseNarrowedVariablePayload(t *testing.T) {
	mod, diag := checkAnalysisSource(t, `enum Choice {
	Left: struct { value: i32 },
	Code: i32,
	Pending,
}
fn read(choice: Choice) -> i32 {
	if choice is Choice::Code { return choice + 1; }
	match choice {
		Choice::Left => { return choice.value; }
		Choice::Code => { return 0; }
		Choice::Pending => { return 0; }
	}
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := mod.AST.Stmts[1].(*ast.FnDecl)
	branch := fn.Body.Stmts[0].(*ast.IfStmt)
	sum := branch.Then.Stmts[0].(*ast.ReturnStmt).Value.(*ast.BinaryExpr)
	code := sum.Left.(*ast.Ident)
	match := fn.Body.Stmts[1].(*ast.MatchStmt)
	left := match.Arms[0].Body.Stmts[0].(*ast.ReturnStmt).Value.(*ast.SelectorExpr).Expr.(*ast.Ident)
	for _, test := range []struct {
		ident    *ast.Ident
		caseIdx  int
		wantType string
	}{
		{code, 1, "i32"},
		{left, 0, "struct{value: i32}"},
	} {
		payload, ok := mod.Analysis.Payload(test.ident.ID())
		if !ok || len(payload.Cases) != 1 || payload.Cases[0] != test.caseIdx || !payload.IsDirect {
			t.Fatalf("payload evidence for case %d = %#v, %v", test.caseIdx, payload, ok)
		}
		if got := typeinfo.TypeText(mod.BaseExprType(test.ident.ID())); got != test.wantType {
			t.Fatalf("narrowed variable type = %s, want %s", got, test.wantType)
		}
	}
}

func TestRunInvalidatesVariantProofAfterMutation(t *testing.T) {
	_, diag := checkAnalysisSource(t, `enum Choice { Left: struct { value: i32 }, Right: struct { value: i32 } }
fn read(mut choice: Choice) -> i32 {
	if choice is Choice::Right {} else {
		choice = Choice::Right with .{ value = 0 };
		return choice.value;
	}
	return 0;
}`)
	if out := diag.EmitAllToString(); !diag.HasErrors() || !strings.Contains(out, "unknown member `value`") {
		t.Fatalf("expected invalidated exact-case field diagnostic, got:\n%s", out)
	}
}

func TestRunPreservesVariantFactsAcrossStableCFGPaths(t *testing.T) {
	_, diag := checkAnalysisSource(t, `enum Choice { Left: struct { value: i32 }, Right: struct { value: i32 } }
struct Holder { choice: Choice }
fn field(holder: Holder) -> i32 {
	if &holder.choice is Choice::Left { return choice.value; }
	return 0;
}
fn loop(choice: Choice) -> i32 {
	for choice is Choice::Left { return choice.value; }
	return 0;
}
fn guard(choice: Choice) -> i32 {
	if !(choice is Choice::Left) { return 0; }
	return choice.value;
}
fn other(choice: Choice) -> i32 {
	if choice is Choice::Left { return 0; } else { return choice.value; }
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected projection/CFG diagnostics:\n%s", diag.EmitAllToString())
	}
}
