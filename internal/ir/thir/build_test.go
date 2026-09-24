package thir_test

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/phase"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/consteval"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/typechecker"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

func buildTypedModule(t *testing.T, source string) *module.Module {
	t.Helper()
	const filePath = "thir_test.peep"
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	mod := &module.Module{
		ID:              moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "thir_test"},
		FilePath:        filePath,
		Content:         source,
		ContentProvided: true,
		AST:             parser.New(filePath, lexer.New(filePath, source, diag).Tokenize(), diag).ParseModule(),
		Imports:         make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(mod)
	collector.Collect(ctx, mod)
	binder.Bind(ctx, mod)
	resolver.Resolve(ctx, mod)
	typechecker.Check(ctx, mod)
	consteval.FinalizeValues(ctx, mod)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	mod.THIR = thir.Build(mod.ID.ImportPath, mod.FilePath, mod.AST, mod.Bindings, mod.Typechecking, nil)
	if err := mod.THIR.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return mod
}

func TestBuildPublishesEffectiveCallsAndPlaces(t *testing.T) {
	mod := buildTypedModule(t, `
struct Point { value: i32 }
fn add(base: i32, step: i32 = base + 1) -> i32 { return base + step; }
fn main() -> i32 {
    let point: Point = .{ value = 4 };
    return add(point.value);
}`)

	var call *thir.Call
	var field *thir.Field
	for _, function := range mod.THIR.Functions {
		if function == nil || function.Body == nil {
			continue
		}
		thir.Inspect(function.Body, func(node thir.Node) bool {
			switch node := node.(type) {
			case *thir.Call:
				call = node
			case *thir.Field:
				field = node
			}
			return true
		})
	}
	if call == nil || len(call.Args) != 2 {
		t.Fatalf("effective call = %#v, want source argument plus expanded default", call)
	}
	if _, ok := call.Args[1].(*thir.Binary); !ok {
		t.Fatalf("expanded default = %T, want *thir.Binary", call.Args[1])
	}
	if field == nil || field.Access == nil || field.Access.Field != 0 || field.ExprPlace() == nil {
		t.Fatalf("resolved field = %#v, want field 0 with explicit place", field)
	}
	if field.ExprPlace().Root == nil || field.ExprPlace().Root.Name != "point" {
		t.Fatalf("field place root = %#v, want point symbol", field.ExprPlace().Root)
	}
	if indexed := mod.THIR.Node(call.SourceInfo().NodeID); indexed != call {
		t.Fatalf("module node index = %T, want call", indexed)
	}
}

func TestBuildPublishesMatchAndRangeIterationPlans(t *testing.T) {
	mod := buildTypedModule(t, `
enum Result { Ok: { value: i32 }, Pending }
fn read(result: Result) -> i32 {
    match result {
        Result::Ok with { value = value } => { return value; }
        Result::Pending => { return 0; }
    }
}
fn main() -> i32 {
    let mut total: i32 = 0;
    for index, value in 0..2 { total = total + index + value; }
    return read(Result::Ok with .{ value = total });
}`)

	var match *thir.Match
	var loop *thir.For
	for _, function := range mod.THIR.Functions {
		if function == nil || function.Body == nil {
			continue
		}
		thir.Inspect(function.Body, func(node thir.Node) bool {
			switch node := node.(type) {
			case *thir.Match:
				match = node
			case *thir.For:
				if node.Iteration != nil {
					loop = node
				}
			}
			return true
		})
	}
	if match == nil || match.CaseCount != 2 || len(match.Arms) != 2 || len(match.Arms[0].Bindings) != 1 {
		t.Fatalf("match evidence = %#v", match)
	}
	if match.Arms[0].Bindings[0].Symbol == nil || match.Arms[0].Bindings[0].Symbol.Name != "value" {
		t.Fatalf("match binding = %#v", match.Arms[0].Bindings[0])
	}
	rangePlan, ok := loop.Iteration.(*thir.RangeIteration)
	if loop == nil || !ok || rangePlan.Cursor == nil || rangePlan.Limit == nil || loop.Index == nil || loop.Value == nil {
		t.Fatalf("range iteration = %#v", loop)
	}
}

func TestBuildValidatesDiscardedMatchPayload(t *testing.T) {
	mod := buildTypedModule(t, `
enum Result { Value: i32, Empty }
fn read(result: Result) -> i32 {
    match result {
        Result::Value with _ => { return 1; }
        Result::Empty => { return 0; }
    }
}`)
	match, ok := mod.THIR.Functions[0].Body.Stmts[0].(*thir.Match)
	if !ok || len(match.Arms[0].Bindings) != 1 {
		t.Fatalf("discarded match evidence = %#v", match)
	}
	binding := &match.Arms[0].Bindings[0]
	if !binding.Discard || binding.Symbol != nil || binding.Type == nil {
		t.Fatalf("discarded binding = %#v", binding)
	}
	binding.Discard = false
	if err := mod.THIR.Validate(); err == nil {
		t.Fatal("missing non-discard symbol was accepted")
	}
	binding.Discard = true
	binding.Type = nil
	if err := mod.THIR.Validate(); err == nil {
		t.Fatal("missing discard type was accepted")
	}
}

func TestBuildNormalizesCheckedCallIteration(t *testing.T) {
	mod := buildTypedModule(t, `
struct Counter { value: i32, limit: i32 }
fn Advance(counter: &mut Counter) -> ?i32 {
    if counter.value >= counter.limit { return none; }
    let value = counter.value;
    counter.value = counter.value + 1;
    return value;
}
fn First() -> i32 {
    let mut counter = Counter.{ value = 0, limit = 1 };
    for item in Advance(&mut counter) { return item; }
    return -1;
}`)

	var checked *thir.For
	for _, function := range mod.THIR.Functions {
		if function == nil || function.Body == nil {
			continue
		}
		thir.Inspect(function.Body, func(node thir.Node) bool {
			if loop, ok := node.(*thir.For); ok && loop.Checked != nil {
				checked = loop
			}
			return true
		})
	}
	if checked == nil || checked.Checked == nil || len(checked.Checked.Stmts) != 1 {
		t.Fatalf("checked iteration = %#v", checked)
	}
	if _, ok := checked.Checked.Stmts[0].(*thir.For); !ok {
		t.Fatalf("checked expansion root = %T, want generated loop", checked.Checked.Stmts[0])
	}
}

func TestBuildPreservesVariantPayloadExpectedType(t *testing.T) {
	mod := buildTypedModule(t, `
enum Wide { Value: i64 }
fn make() -> Wide { return Wide::Value with 7i32; }
`)

	var variant *thir.Variant
	for _, function := range mod.THIR.Functions {
		if function == nil || function.Body == nil {
			continue
		}
		thir.Inspect(function.Body, func(node thir.Node) bool {
			if construction, ok := node.(*thir.Variant); ok {
				variant = construction
			}
			return true
		})
	}
	if variant == nil {
		t.Fatal("variant construction missing from THIR")
	}
	if got := typeinfo.TypeText(variant.PayloadType); got != "i64" {
		t.Fatalf("variant payload expected type = %s, want i64", got)
	}
	if got := typeinfo.TypeText(variant.Payload.ExprType()); got != "i32" {
		t.Fatalf("variant payload source type = %s, want i32", got)
	}
}

func TestModuleResetDropsTHIRBelowTypechecked(t *testing.T) {
	mod := buildTypedModule(t, `fn main() -> i32 { return 0; }`)
	if mod.THIR == nil {
		t.Fatal("THIR was not built")
	}
	mod.Phase = phase.Typechecked
	mod.ResetToPhase(phase.Resolved)
	if mod.THIR != nil {
		t.Fatal("THIR survived reset below Typechecked")
	}
}
