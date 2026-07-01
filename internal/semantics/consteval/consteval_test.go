package consteval

import (
	"testing"

	"compiler/internal/constvalue"
	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/pkg/peeper"
)

func constevalModule(t *testing.T, src string) (*project.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "consteval_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, src)
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "consteval_test",
		FilePath:   filePath,
		Content:    src,
		AST:        parser.New(filePath, lexer.New(filePath, src, diag).Tokenize(), diag).ParseModule(),
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	Evaluate(ctx, module)
	return module, diag
}

func TestEvaluateTopLevelConstExpressions(t *testing.T) {
	module, diag := constevalModule(t, `const A = 1 + 2 * 3;
const B = A + 4;
const C = true && false;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "A", "7", "")
	assertIntConst(t, module, "B", "11", "")
	assertBoolConst(t, module, "C", false)
}

func TestEvaluateReportsConstCycle(t *testing.T) {
	_, diag := constevalModule(t, `const A = B;
const B = A;
`)
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == diagnostics.ErrCircularDependency {
			return
		}
	}
	t.Fatalf("expected circular dependency diagnostic, got:\n%s", diag.EmitAllToString())
}

func TestEvaluateUsesDeclaredTypeForNumericConst(t *testing.T) {
	module, diag := constevalModule(t, `const A: i64 = 1;
const B = A + 2147483648;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "A", "1", "i64")
	assertIntConst(t, module, "B", "2147483649", "i64")
}

func TestEvaluateUsesDeclaredTypeForNumericExpression(t *testing.T) {
	module, diag := constevalModule(t, `const A: i64 = 1 + 2;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "A", "3", "i64")
}

func TestEvaluateUsesConstOperandTypeForSmallLiteral(t *testing.T) {
	module, diag := constevalModule(t, `const A: i64 = 1;
const B = A + 1;
const C = 1 + A;
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "B", "2", "i64")
	assertIntConst(t, module, "C", "2", "i64")
}

func TestEvaluateUsesConstOperandTypeForNestedArithmetic(t *testing.T) {
	module, diag := constevalModule(t, `const A: i64 = 1;
const B = A + (1 + 2);
`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	assertIntConst(t, module, "B", "4", "i64")
}

func assertIntConst(t *testing.T, module *project.Module, name, want, wantType string) {
	t.Helper()
	sym, ok := module.ModuleScope.LookupLocal(name)
	if !ok || sym == nil {
		t.Fatalf("missing symbol %s", name)
	}
	got, ok := module.Semantics.ConstValues[sym.ID].(*constvalue.IntConst)
	if !ok || got == nil || got.Value != want || (wantType != "" && got.TypeText() != wantType) {
		t.Fatalf("%s = %#v, want int %s %s", name, module.Semantics.ConstValues[sym.ID], want, wantType)
	}
}

func assertBoolConst(t *testing.T, module *project.Module, name string, want bool) {
	t.Helper()
	sym, ok := module.ModuleScope.LookupLocal(name)
	if !ok || sym == nil {
		t.Fatalf("missing symbol %s", name)
	}
	got, ok := module.Semantics.ConstValues[sym.ID].(*constvalue.BoolConst)
	if !ok || got == nil || got.Value != want {
		t.Fatalf("%s = %#v, want bool %v", name, module.Semantics.ConstValues[sym.ID], want)
	}
}
