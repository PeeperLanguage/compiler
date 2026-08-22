package definiteinit

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir"
	"compiler/internal/ir/cfg"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

func analyzeInitializationSource(t *testing.T, source string) (*functionResult, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "definite_init_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, source)
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &project.Module{
		Key:        project.ModuleKeyFor(project.ModuleOriginLocal, filePath),
		ImportPath: "definite_init_test",
		FilePath:   filePath,
		Content:    source,
		AST:        parser.New(filePath, lexer.New(filePath, source, diag).Tokenize(), diag).ParseModule(),
		Imports:    make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	typechecker.Check(ctx, module)
	module.TypedASTNodes = ast.Index(module.AST)
	module.CFG = cfg.BuildModule(module.AST)
	symbol, found := module.ModuleScope.Lookup("choose")
	if !found || symbol == nil {
		t.Fatal("choose function symbol missing")
	}
	fn, ok := symbol.ASTNode.(*ast.FnDecl)
	if !ok || fn == nil {
		t.Fatal("choose function AST missing")
	}
	graph := module.CFG.Function(ir.NodeID(fn.ID()))
	if graph == nil {
		t.Fatal("choose function CFG missing")
	}
	result := analyzeFunction(
		fn,
		graph,
		module.TypedASTNodes,
		module.Semantics.BlockScopes,
		module.Semantics.ResolvedSymbols,
		diag,
	)
	return result, diag
}

func TestInitializationIgnoresTerminatingBranchAtJoin(t *testing.T) {
	result, diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	if flag {
		value = 7;
	} else {
		return 3;
	}
	return value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if result == nil || len(result.In) == 0 || len(result.Out) == 0 {
		t.Fatalf("initialization result = %#v, want per-site states", result)
	}
}

func TestInitializationRejectsContinuingUninitializedBranch(t *testing.T) {
	_, diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	if flag {
		value = 7;
	}
	return value;
}`)
	if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized diagnostic:\n%s", diag.EmitAllToString())
	}
	if got := diag.EmitAllToString(); !strings.Contains(got, "symbol `value` used before it's initialized") || strings.Contains(got, "value$") {
		t.Fatalf("diagnostic does not use source symbol name:\n%s", got)
	}
}

func TestInitializationAcceptsAssignmentOnBothBranches(t *testing.T) {
	_, diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	if flag {
		value = 7;
	} else {
		value = 3;
	}
	return value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationLoopMayExecuteZeroTimes(t *testing.T) {
	_, diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	for flag {
		value = 7;
	}
	return value;
}`)
	if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized diagnostic:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationAcceptsDirectAssignment(t *testing.T) {
	_, diag := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	value = 7;
	return value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func hasDiagnosticCode(diag *diagnostics.DiagnosticBag, code string) bool {
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}
