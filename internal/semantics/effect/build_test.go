package effect_test

import (
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

func buildEffects(t *testing.T, source string) (effect.Result, *project.Module) {
	t.Helper()
	const filePath = "effect_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, source)
	ctx := project.New(".", peeper.SourceExt, diag)
	module := &project.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "effect_test"},
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
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	result := effect.Build(module.CFG, module.TypedASTNodes, effect.BuildQueries{
		Symbols:       module.Bindings.NodeSymbols,
		Scopes:        module.Bindings.BlockScopes,
		CallArguments: module.Typechecking.CallArgumentsOrSource,
		ArmBindings:   module.Typechecking.ArmBindings,
	})
	if result == nil {
		t.Fatal("Build published no result")
	}
	return result, module
}

// publishedOps flattens one function's effects in site order so a test can
// state the published sequence without naming SiteIDs.
func publishedOps(t *testing.T, result effect.Result, module *project.Module, name string) []string {
	t.Helper()
	symbol, found := module.ModuleScope.Lookup(name)
	if !found || symbol == nil {
		t.Fatalf("function %q missing", name)
	}
	fn, ok := symbol.ASTNode.(*ast.FnDecl)
	if !ok || fn == nil {
		t.Fatalf("function %q has no declaration", name)
	}
	graph := module.CFG.Function(ir.NodeID(fn.ID()))
	if graph == nil {
		t.Fatalf("function %q has no CFG", name)
	}
	published := make([]string, 0)
	for _, block := range graph.Blocks {
		if block == nil || !block.Reachable {
			continue
		}
		for _, site := range block.Sites {
			if site == nil {
				continue
			}
			for _, op := range result.At(graph.NodeID, site.ID) {
				published = append(published, describe(op))
			}
		}
	}
	return published
}

func describe(op effect.Op) string {
	switch op := op.(type) {
	case effect.Define:
		if op.Initialized {
			return "define " + op.Symbol.Name
		}
		return "declare " + op.Symbol.Name
	case effect.Write:
		return "write " + op.Symbol.Name
	case effect.Use:
		return "use " + op.Symbol.Name
	}
	return "unknown"
}

func sameOps(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Parameters are initialized on entry, and a binding's initializer is read
// before the binding it defines. That order is what makes `let x = x` resolve
// against an outer binding rather than itself.
func TestBuildPublishesReadsBeforeTheDefineTheyInitialize(t *testing.T) {
	result, module := buildEffects(t, `fn add(a: i32, b: i32) -> i32 {
	let total = a + b;
	return total;
}`)
	got := publishedOps(t, result, module, "add")
	want := []string{
		"define a", "define b",
		"use a", "use b", "define total",
		"use total",
	}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

// An assignment reads its value before it writes its target.
func TestBuildPublishesAssignmentAsReadThenWrite(t *testing.T) {
	result, module := buildEffects(t, `fn bump(start: i32) -> i32 {
	let mut count = start;
	count = count + 1;
	return count;
}`)
	got := publishedOps(t, result, module, "bump")
	want := []string{
		"define start",
		"use start", "define count",
		"use count", "write count",
		"use count",
	}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

// A declaration with no initializer declares storage without initializing it.
// That distinction is the whole basis of definite initialization.
func TestBuildDistinguishesUninitializedDeclaration(t *testing.T) {
	result, module := buildEffects(t, `fn choose(flag: bool) -> i32 {
	let mut value: i32;
	if flag {
		value = 7;
	} else {
		value = 3;
	}
	return value;
}`)
	got := publishedOps(t, result, module, "choose")
	want := []string{
		"define flag",
		"declare value",
		"use flag",
		"write value",
		"write value",
		"use value",
	}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

// A branch condition belongs to the terminator site, not to the statement, so
// each site carries exactly the reads that happen at it.
func TestBuildPublishesBranchConditionAtTerminatorSite(t *testing.T) {
	result, module := buildEffects(t, `fn gate(flag: bool) -> i32 {
	if flag {
		return 1;
	}
	return 0;
}`)
	got := publishedOps(t, result, module, "gate")
	want := []string{"define flag", "use flag"}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}

// An arm's payload binding is published before that arm body's own effects,
// because the body may read the payload it binds. The binding is published at
// the arm block's first site rather than on the case edge, which is equivalent
// because CFG construction gives every arm a fresh block reached only by its
// own case edge.
func TestBuildPublishesArmBindingBeforeArmBodyEffects(t *testing.T) {
	result, module := buildEffects(t, `enum Result {
	Ok: { value: i32 },
	Pending,
}

fn choose(outcome: Result) -> i32 {
	match outcome {
		Result::Ok with { value = payload } => {
			return payload;
		}
		Result::Pending => {
			return 0;
		}
	}
}`)
	got := publishedOps(t, result, module, "choose")
	// The match subject is deliberately absent. Definite initialization attaches
	// a site condition for a branch terminator only, so a match on an
	// uninitialized value is not diagnosed today. Publishing that read here
	// would start rejecting code that currently compiles, so it is registered as
	// a behavior change in docs/compiler-framework/effect-stream-migration.md
	// and left for separate approval. Change this expectation only together with
	// that decision.
	want := []string{
		"define outcome",
		"define payload", "use payload",
	}
	if !sameOps(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
}
