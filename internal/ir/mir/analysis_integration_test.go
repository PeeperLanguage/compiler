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
	instructions := lowerMainInstructions(t, "analysis_cleanup_test", source)
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

func TestRvalueIdentifierIsMaterializedBeforeLaterCallSideEffect(t *testing.T) {
	const source = `#[extern("memset")]
fn Memset(pointer: rawptr, value: i32, size: usize) -> rawptr;
fn Clear(pointer: rawptr) -> i32 { Memset(pointer, 0, 4); return 0; }
fn Sum(first: i32, second: i32) -> i32 { return first + second; }
fn Original(value: i32) -> i32 { return value + 7; }
fn Invoke(callback: fn(i32) -> i32, value: i32) -> i32 { return callback(value); }
`
	for _, test := range []struct {
		name   string
		body   string
		callee bool
	}{
		{name: "scalar_argument", body: "let mut value: i32 = 7; Sum(value, Clear(@value));"},
		{name: "callable_argument", body: "let mut value = Original; let address = @value; Invoke(value, Clear(address));"},
		{name: "callee", body: "let mut value = Original; value(Clear(@value));", callee: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			instructions := lowerMainInstructions(t, "rvalue_eval_order_test", source+"fn main() {"+test.body+"}")
			callIndex := -1
			sideEffectIndex := -1
			var addressedRoot mir.ValueRef
			var firstArg *mir.RefName
			for index, instruction := range instructions {
				var call *mir.Call
				switch instruction := instruction.(type) {
				case *mir.Call:
					call = instruction
				case *mir.Assign:
					call, _ = instruction.Value.(*mir.Call)
					if address, ok := instruction.Value.(*mir.AddrOf); ok {
						addressedRoot = address.Place.Root
					}
				}
				if call == nil {
					continue
				}
				if sideEffectIndex < 0 {
					sideEffectIndex = index
					continue
				}
				callIndex = index
				captured := call.Callee
				if !test.callee {
					if len(call.Args) != 2 {
						t.Fatalf("consumer has %d arguments, want 2", len(call.Args))
					}
					captured = call.Args[0]
				}
				var ok bool
				firstArg, ok = captured.(*mir.RefName)
				if !ok {
					t.Fatalf("captured operand = %#v, want materialized reference", captured)
				}
				break
			}
			if callIndex < 0 || firstArg == nil {
				t.Fatal("MIR did not contain consuming call")
			}

			if sideEffectIndex < 0 || sideEffectIndex >= callIndex || addressedRoot == nil {
				t.Fatal("missing Clear call and original storage address before consumer")
			}
			for index := 0; index < sideEffectIndex; index++ {
				assignment, ok := instructions[index].(*mir.Assign)
				if !ok || assignment.Name != firstArg.Name {
					continue
				}
				if move, ok := assignment.Value.(*mir.Move); ok && move.Src.Text() == addressedRoot.Text() {
					return
				}
			}
			t.Fatalf("operand %q was not captured from original storage before Clear", firstArg.Name)
		})
	}
}

func lowerMainInstructions(t *testing.T, name, source string) []mir.Instr {
	t.Helper()
	filePath := name + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	diag.AddSourceContent(filePath, source)
	ctx := project.New(".", peeper.SourceExt, diag)
	mod := &module.Module{
		ID:       moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: name},
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
	for _, fn := range out.Funcs {
		if fn.Name == "main" && len(fn.Blocks) > 0 {
			return fn.Blocks[0].Instrs
		}
	}
	t.Fatal("MIR has no main entry block")
	return nil
}
