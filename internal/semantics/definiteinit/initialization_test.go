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
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/effect"
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
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "definite_init_test"},
		FilePath: filePath,
		Content:  source,
		AST:      parser.New(filePath, lexer.New(filePath, source, diag).Tokenize(), diag).ParseModule(),
		Imports:  make(map[string]project.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	binder.Bind(ctx, module)
	resolver.Resolve(ctx, module)
	typechecker.Check(ctx, module)
	module.RebuildTypedASTIndex()
	module.CFG = cfg.BuildModule(module.AST, cfg.BuildQueries{
		MatchCases:          module.Typechecking.MatchCases,
		LoopGuaranteedEntry: module.Typechecking.ForLoopGuaranteedEntry,
	})
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
	effects := effect.Build(module.CFG, module.TypedASTNodes, effect.BuildQueries{
		Symbols:       module.Bindings.NodeSymbols,
		Scopes:        module.Bindings.BlockScopes,
		CallArguments: module.Typechecking.CallArgumentsOrSource,
		ArmBindings:   module.Typechecking.ArmBindings,
	})
	result := analyzeFunction(graph, effects[graph.NodeID], diag)
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

func TestInitializationUsesGuaranteedRangeEntry(t *testing.T) {
	for _, test := range []struct {
		name      string
		rangeText string
		wantError bool
	}{
		{name: "guaranteed entry", rangeText: "0..1"},
		{name: "empty range", rangeText: "0..0", wantError: true},
		{name: "runtime range", rangeText: "start..end", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, diag, _ := analyzeInitializationSource(t, `fn choose(start: i32, end: i32) -> i32 {
	let mut value: i32;
	for item in `+test.rangeText+` {
		value = item;
	}
	return value;
}`)
			if got := hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable); got != test.wantError {
				t.Fatalf("uninitialized diagnostic = %v, want %v:\n%s", got, test.wantError, diag.EmitAllToString())
			}
		})
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
	binding := module.Bindings.NodeSymbols[match.Arms[0].Fields[0].Binding.ID()]
	returnID := ir.NodeID(match.Arms[0].Body.Stmts[0].ID())
	for _, block := range module.CFG.Function(ir.NodeID(fn.ID())).Blocks {
		for _, cfgSite := range block.Sites {
			if cfgSite.NodeID != returnID {
				continue
			}
			// The binding is published as an initialized define at the arm
			// block's first site, which is this return's own site, so it lands
			// in Out rather than In. It used to be applied on the case edge and
			// so appeared in In. The read of `payload` in the arm body is
			// covered either way, because a site's effects are replayed in
			// evaluation order and the define precedes the read.
			if _, initialized := result.Out[cfgSite.ID][binding.ID]; !initialized {
				t.Fatalf("pattern binding absent at arm return: state=%#v", result.Out[cfgSite.ID])
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

// A match subject is a read like any other. This was previously undiagnosed:
// the analysis attached a site condition for a branch terminator only, so
// nothing checked the subject of a match. Publishing the subject as an ordinary
// read closed the gap, and this analysis learned nothing about matches to get it.
func TestInitializationChecksMatchSubject(t *testing.T) {
	_, diag, _ := analyzeInitializationSource(t, `enum Outcome {
	Ok: { value: i32 },
	Pending,
}

fn choose(flag: bool) -> i32 {
	let mut outcome: Outcome;
	if flag {
		outcome = Outcome::Pending;
	}
	match outcome {
		Outcome::Ok with { value = payload } => {
			return payload;
		}
		Outcome::Pending => {
			return 0;
		}
	}
}`)
	if !hasDiagnosticCode(diag, diagnostics.ErrUninitializedVariable) {
		t.Fatalf("expected uninitialized match subject diagnostic:\n%s", diag.EmitAllToString())
	}
	if got := diag.EmitAllToString(); !strings.Contains(got, "symbol `outcome` used before it's initialized") {
		t.Fatalf("diagnostic does not name the match subject:\n%s", got)
	}
}
