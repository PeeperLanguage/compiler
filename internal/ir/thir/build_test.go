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
		ID:                 moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "thir_test"},
		FilePath:           filePath,
		Content:            source,
		HasProvidedContent: true,
		AST:                parser.New(filePath, lexer.New(filePath, source, diag).Tokenize(), diag).ParseModule(),
		Imports:            make(map[string]module.ResolvedImport),
	}
	ctx.AddModule(mod)

	collector.Collect(ctx, mod)
	binder.Bind(ctx, mod)
	resolver.Resolve(ctx, mod)
	mod.THIR = typechecker.Check(ctx, mod)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if err := mod.THIR.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return mod
}

func TestBuildPublishesStableFunctionIdentityAcrossEarlierBodyEdit(t *testing.T) {
	first := buildTypedModule(t, "fn Prep() {}\nfn main() -> i32 { return 7; }\n")
	second := buildTypedModule(t, "fn Prep() { let x = 1; let y = 2; }\nfn main() -> i32 { return 7; }\n")
	if len(first.THIR.Functions) != 2 || len(second.THIR.Functions) != 2 {
		t.Fatalf("function counts = %d and %d, want two", len(first.THIR.Functions), len(second.THIR.Functions))
	}
	firstID := first.THIR.Functions[1].Identity
	secondID := second.THIR.Functions[1].Identity
	if first.THIR.Functions[1].Source.NodeID != second.THIR.Functions[1].Source.NodeID {
		t.Fatalf("later function source identity changed from %v to %v", first.THIR.Functions[1].Source.NodeID, second.THIR.Functions[1].Source.NodeID)
	}
	if firstID == "" || firstID != secondID {
		t.Fatalf("later function identities = %q and %q, want stable non-empty identity", firstID, secondID)
	}
}

func TestFunctionByIDFindsPublishedFunction(t *testing.T) {
	mod := buildTypedModule(t, "fn first() {}\nfn second() {}")
	function := mod.THIR.Functions[1]
	if got := mod.THIR.FunctionByID(function.Identity); got != function {
		t.Fatalf("FunctionByID(%q) = %p, want %p", function.Identity, got, function)
	}
}

func TestValidateRejectsStaleFunctionIdentityIndex(t *testing.T) {
	mod := buildTypedModule(t, "fn first() {}\nfn second() {}")
	function := mod.THIR.Functions[1]
	identity := function.Identity
	function.Identity = "changed"
	if got := mod.THIR.FunctionByID(identity); got != nil {
		t.Fatalf("FunctionByID(%q) = %p after identity changed, want nil", identity, got)
	}
	if err := mod.THIR.Validate(); err == nil {
		t.Fatal("stale function identity index accepted")
	}
}

func TestValidateRejectsMissingFunctionIdentity(t *testing.T) {
	mod := buildTypedModule(t, "fn main() -> i32 { return 0; }")
	mod.THIR.Functions[0].Identity = ""
	if err := mod.THIR.Validate(); err == nil {
		t.Fatal("missing function identity accepted")
	}
}

func TestValidateRejectsDuplicateFunctionIdentity(t *testing.T) {
	mod := buildTypedModule(t, "fn first() {}\nfn second() {}")
	mod.THIR.Functions[1].Identity = mod.THIR.Functions[0].Identity
	if err := mod.THIR.Validate(); err == nil {
		t.Fatal("duplicate function identity accepted")
	}
}

func TestValidateRejectsDuplicateFunctionSourceIdentity(t *testing.T) {
	mod := buildTypedModule(t, "fn first() {}\nfn second() {}")
	mod.THIR.Functions[1].Source.NodeID = mod.THIR.Functions[0].Source.NodeID
	if err := mod.THIR.Validate(); err == nil {
		t.Fatal("duplicate function source identity accepted")
	}
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
enum Result { Ok: struct { value: i32 }, Pending }
fn read(result: Result) -> i32 {
    match result {
        Result::Ok => { return result.value; }
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
	if match == nil || match.CaseCount != 2 || len(match.Arms) != 2 || match.Arms[0].Cases[0].Case != 0 || match.Arms[1].Cases[0].Case != 1 {
		t.Fatalf("match evidence = %#v", match)
	}
	narrowed, _ := match.Arms[0].Body.Stmts[0].(*thir.Return).Value.(*thir.Field).Base.(*thir.Ident)
	if narrowed == nil || typeinfo.TypeText(narrowed.CaseCarrier) != "Result" || typeinfo.TypeText(narrowed.Type) != "struct{value: i32}" {
		t.Fatalf("arm subject = %#v, want case payload", narrowed)
	}
	rangePlan, ok := loop.Iteration.(*thir.RangeIteration)
	if loop == nil || !ok || rangePlan.Cursor == nil || rangePlan.Limit == nil || loop.Index == nil || loop.Value == nil {
		t.Fatalf("range iteration = %#v", loop)
	}
}

func TestBuildPublishesAndValidatesDestructureBindings(t *testing.T) {
	mod := buildTypedModule(t, `
struct Pair { count: i32, name: str }
fn read(pair: Pair) -> i32 {
    let .{ count, _ } = pair;
    return count;
}`)
	destructure, ok := mod.THIR.Functions[0].Body.Stmts[0].(*thir.Destructure)
	if !ok || len(destructure.Bindings) != 1 {
		t.Fatalf("destructure evidence = %#v", mod.THIR.Functions[0].Body.Stmts[0])
	}
	binding := &destructure.Bindings[0]
	if binding.Field != 0 || binding.Symbol == nil || binding.Symbol.Name != "count" || typeinfo.TypeText(binding.Type) != "i32" {
		t.Fatalf("destructure binding = %#v", binding)
	}
	binding.Symbol = nil
	if err := mod.THIR.Validate(); err == nil {
		t.Fatal("destructure binding without a symbol was accepted")
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

func TestBuildReadsTestedVariablePayloadInPlace(t *testing.T) {
	mod := buildTypedModule(t, `
struct Box { value: i32 }
enum Slot { Owned: *Box, Plain: i32 }
fn Take(slot: Slot) -> i32 {
    match slot {
        Slot::Owned => { free(slot); return 1; }
        Slot::Plain => { return slot; }
    }
}`)
	// No hidden local: the match reads the variable, and each use of its name
	// in an arm is a payload read of that variable.
	match, ok := mod.THIR.Functions[0].Body.Stmts[0].(*thir.Match)
	if !ok || match.Subject.(*thir.Ident).Name != "slot" {
		t.Fatalf("statement = %#v, want a match on slot", mod.THIR.Functions[0].Body.Stmts[0])
	}
	subject := match.Subject.(*thir.Ident)
	plain, _ := match.Arms[1].Body.Stmts[0].(*thir.Return).Value.(*thir.Ident)
	if plain == nil || plain.Symbol != subject.Symbol || typeinfo.TypeText(plain.Type) != "i32" || typeinfo.TypeText(plain.CaseCarrier) != "Slot" {
		t.Fatalf("plain arm read = %#v, want the i32 payload of slot", plain)
	}
}

func TestBuildMatchesBorrowedSubjectInPlace(t *testing.T) {
	mod := buildTypedModule(t, `
enum Shape { Round: struct { radius: i32 }, Empty }
struct Holder { shape: Shape }
fn Grow(holder: &mut Holder) {
    match &mut holder.shape {
        Shape::Round => { shape.radius = 2; }
        Shape::Empty => { }
    }
}`)
	wrapper := mod.THIR.Functions[0].Body.Stmts[0].(*thir.Block)
	// No carrier: the match reads the place itself.
	match, _ := wrapper.Stmts[0].(*thir.Match)
	if len(wrapper.Stmts) != 1 || match == nil {
		t.Fatalf("statement = %#v, want only the match", wrapper.Stmts)
	}
	subject, _ := match.Subject.(*thir.Field)
	if subject == nil || subject.Name != "shape" || subject.CaseCarrierType() != nil {
		t.Fatalf("subject = %#v, want the whole field", match.Subject)
	}
	shape, _ := match.Arms[0].Body.Stmts[0].(*thir.Binding)
	address, _ := shape.Value.(*thir.Address)
	if shape == nil || address == nil || address.Mode != thir.AddressMutable || typeinfo.TypeText(shape.Symbol.Type) != "&mut struct{radius: i32}" {
		t.Fatalf("payload name = %#v", shape)
	}
	payload, _ := address.Value.(*thir.Field)
	if payload == nil || payload == subject || payload.SourceInfo().NodeID == subject.SourceInfo().NodeID || payload.CaseCarrierType() == nil {
		t.Fatalf("borrowed payload = %#v, want a second read of the field", address.Value)
	}
	if len(match.Arms[1].Body.Stmts) != 0 {
		t.Fatalf("empty arm = %#v, want nothing dropped", match.Arms[1].Body.Stmts)
	}
}
