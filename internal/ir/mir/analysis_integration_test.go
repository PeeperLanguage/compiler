package mir_test

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir/cfg"
	"compiler/internal/ir/mir"
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

func TestDiscardedCallDropsTemporariesBeforeResult(t *testing.T) {
	const source = `fn make() -> i32;
fn consume(first: &i32, second: &i32) -> *i32;
fn main() { consume(&make(), &make()); }`
	const filePath = "analysis_cleanup_test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, source)
	ctx := project.New(".", peeper.SourceExt, diag)
	mod := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "analysis_cleanup_test"},
		FilePath: filePath, Content: source,
		AST:     parser.New(filePath, lexer.New(filePath, source, diag).Tokenize(), diag).ParseModule(),
		Imports: make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(mod)
	collector.Collect(ctx, mod)
	binder.Bind(ctx, mod)
	resolver.Resolve(ctx, mod)
	mod.THIR = typechecker.Check(ctx, mod)
	mod.CFG = cfg.BuildModule(mod.THIR)
	mod.Analysis = analysis.Run(diag, analysis.Input{Source: mod.THIR, CFG: mod.CFG, Scope: mod.ModuleScope, SymbolIndex: mod.SymbolIndex})
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}

	out := mir.GenerateMIR(mir.LoweringInput{
		Types: ctx.Types, Source: mod.THIR, CFG: mod.CFG, Analysis: mod.Analysis,
		Scope: mod.ModuleScope, SymbolIndex: mod.SymbolIndex, ModuleID: mod.ID, IsEntryModule: true,
	})
	if out == nil {
		t.Fatal("MIR lowering returned nil")
	}
	var instructions []mir.Instr
	for _, fn := range out.Funcs {
		if fn.Name == "main" && len(fn.Blocks) > 0 {
			instructions = fn.Blocks[0].Instrs
			break
		}
	}
	if len(instructions) != 8 {
		t.Fatalf("main instructions = %#v, want two temporary calls, two borrows, call, and three drops", instructions)
	}
	firstBorrow, ok := instructions[1].(*mir.Assign)
	if !ok {
		t.Fatalf("second instruction = %T, want borrow assignment", instructions[1])
	}
	secondBorrow, ok := instructions[3].(*mir.Assign)
	if !ok {
		t.Fatalf("fourth instruction = %T, want borrow assignment", instructions[3])
	}
	firstAddress, ok := firstBorrow.Value.(*mir.AddrOf)
	if !ok {
		t.Fatalf("first value = %T, want address", firstBorrow.Value)
	}
	secondAddress, ok := secondBorrow.Value.(*mir.AddrOf)
	if !ok {
		t.Fatalf("second value = %T, want address", secondBorrow.Value)
	}
	called, ok := instructions[4].(*mir.Assign)
	if !ok {
		t.Fatalf("fifth instruction = %T, want call assignment", instructions[4])
	}
	invocation, ok := called.Value.(*mir.Call)
	if !ok || len(invocation.Args) != 2 {
		t.Fatalf("invocation = %#v, want two arguments", called.Value)
	}
	if firstArg, ok := invocation.Args[0].(*mir.RefName); !ok || firstArg.Name != firstBorrow.Name {
		t.Fatalf("first argument = %#v, want first borrowed temporary", invocation.Args[0])
	}
	if secondArg, ok := invocation.Args[1].(*mir.RefName); !ok || secondArg.Name != secondBorrow.Name {
		t.Fatalf("second argument = %#v, want second borrowed temporary", invocation.Args[1])
	}
	for index, want := range []mir.ValueRef{secondAddress.Place.Root, firstAddress.Place.Root} {
		drop, ok := instructions[index+5].(*mir.Drop)
		if !ok || drop.Value != want {
			t.Fatalf("temporary cleanup %d = %#v, want borrowed owner %#v", index, instructions[index+5], want)
		}
	}
	resultDrop, ok := instructions[7].(*mir.Drop)
	if !ok {
		t.Fatalf("result cleanup = %T, want drop", instructions[7])
	}
	value, ok := resultDrop.Value.(*mir.RefName)
	if !ok || value.Name != called.Name {
		t.Fatalf("result cleanup = %#v, want %s", resultDrop.Value, called.Name)
	}
}
