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

func analyzeInitializationSource(t *testing.T, source string) (*functionResult, *diagnostics.DiagnosticBag, *project.Module) {
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
	module.CFG = cfg.BuildModule(module.AST, module.Semantics.MatchCases)
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
		module.Semantics.Matches,
		diag,
	)
	return result, diag, module
}

func TestInitializationIgnoresTerminatingBranchAtJoin(t *testing.T) {
	result, diag, _ := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
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
	_, diag, _ := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
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
	_, diag, _ := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
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
	_, diag, _ := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
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
	_, diag, _ := analyzeInitializationSource(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	value = 7;
	return value;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInitializationDefinesMatchPatternBindingOnCaseEdge(t *testing.T) {
	result, diag, module := analyzeInitializationSource(t, `enum Result {
	Ok: { value: i32 },
	Pending,
}

fn choose(result: Result) -> i32 {
	match result {
		Result::Ok with { value = payload } => {
			return payload;
		}
		Result::Pending => {
			return 0;
		}
	}
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[1].(*ast.FnDecl)
	match := fn.Body.Stmts[0].(*ast.MatchStmt)
	binding := module.Semantics.ResolvedSymbols[match.Arms[0].Fields[0].Binding.ID()]
	returnID := ir.NodeID(match.Arms[0].Body.Stmts[0].ID())
	for _, block := range module.CFG.Function(ir.NodeID(fn.ID())).Blocks {
		for _, cfgSite := range block.Sites {
			if cfgSite.NodeID != returnID {
				continue
			}
			if _, initialized := result.In[cfgSite.ID][binding.ID]; !initialized {
				t.Fatalf("pattern binding absent at arm return: state=%#v", result.In[cfgSite.ID])
			}
			return
		}
	}
	t.Fatal("match arm return site missing")
}

func hasDiagnosticCode(diag *diagnostics.DiagnosticBag, code string) bool {
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}
