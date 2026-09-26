package exprlower_test

import (
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
	"compiler/internal/ir"
	"compiler/internal/ir/exprlower"
	"compiler/internal/ir/thir"
	"compiler/internal/module"
	"compiler/internal/moduleid"
	"compiler/internal/project"
	"compiler/internal/semantics/binder"
	"compiler/internal/semantics/collector"
	"compiler/internal/semantics/resolver"
	"compiler/internal/semantics/symbols"
	"compiler/internal/semantics/typechecker"
	"compiler/internal/semantics/typeinfo"
	"compiler/pkg/peeper"
)

func buildTypedExprModule(t *testing.T, source string) (*module.Module, *diagnostics.DiagnosticBag) {
	t.Helper()
	const filePath = "exprlower_test.peep"
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	mod := &module.Module{
		ID:                 moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "exprlower_test"},
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
	typechecker.Check(ctx, mod)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	mod.THIR = thir.Build(mod.ID, mod.FilePath, mod.AST, mod.Bindings, mod.Typechecking, nil)
	if err := mod.THIR.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return mod, diag
}

func TestLowerImplicitReferenceRetainsTemporaryOnlyForValues(t *testing.T) {
	integer := &typeinfo.IntegerType{IsSigned: true, Bits: 32}
	borrow := &typeinfo.RefType{Target: integer}
	ctx := exprlower.Context{Types: ir.NewTypeTable()}
	literal := &thir.NumberLiteral{ExprInfo: thir.ExprInfo{Source: ir.SourceInfo{NodeID: 1}, Type: integer}, Value: "3"}
	temporary, ok := exprlower.LowerImplicitReference(ctx, literal, borrow).(*ir.TempBorrow)
	if !ok || temporary.Value.TypeID() == ir.InvalidType || temporary.Origin().NodeID != 1 {
		t.Fatalf("borrowed literal = %#v, want temporary owner with source identity", temporary)
	}
	symbol := symbols.New("value", symbols.SymbolVar, nil, nil)
	symbol.Type = integer
	ident := &thir.Ident{ExprInfo: thir.ExprInfo{
		Source: ir.SourceInfo{NodeID: 2}, Type: integer, Place: &thir.Place{Root: symbol, Type: integer},
	}, Symbol: symbol, Name: symbol.Name}
	address, ok := exprlower.LowerImplicitReference(ctx, ident, borrow).(*ir.AddrOf)
	if !ok || address.Place == nil || address.Origin().NodeID != 2 {
		t.Fatalf("borrowed place = %#v, want addressable storage", address)
	}
	root, ok := address.Place.Root.(*ir.Ident)
	if !ok || root.SymbolID != symbol.ID {
		t.Fatalf("borrowed root = %#v, want original symbol", address.Place.Root)
	}
}

func TestLowerVariantConvertsPayloadToExpectedType(t *testing.T) {
	mod, diag := buildTypedExprModule(t, `
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

	types := ir.NewTypeTable()
	lowered, ok := exprlower.Lower(exprlower.Context{
		Types: types, Diagnostics: diag, Source: mod.THIR, ModuleID: mod.ID,
	}, variant, nil).(*ir.VariantMake)
	if !ok {
		t.Fatalf("lowered variant = %T, want *ir.VariantMake", lowered)
	}
	conversion, ok := lowered.Payload.(*ir.Cast)
	if !ok {
		t.Fatalf("lowered payload = %T, want numeric cast", lowered.Payload)
	}
	if got := types.Text(conversion.TypeID()); got != "i64" {
		t.Fatalf("payload conversion type = %s, want i64", got)
	}
	if got := types.Text(conversion.Expr.TypeID()); got != "i32" {
		t.Fatalf("payload source type = %s, want i32", got)
	}
}

func TestLowerInterfaceUsesPublishedImplementation(t *testing.T) {
	mod, diag := buildTypedExprModule(t, `
iface Summer { fn (&Self) sum() -> i32 }
struct Point { value: i32 }
fn (self: &Point) sum() -> i32 { return self.value; }
fn consume(value: &Summer) -> i32 { return value.sum(); }
fn main() -> i32 {
    let point: Point = .{ value = 7 };
    return consume(&point);
}`)
	var call *thir.Call
	for _, function := range mod.THIR.Functions {
		if function.Name != "main" {
			continue
		}
		thir.Inspect(function.Body, func(node thir.Node) bool {
			if current, ok := node.(*thir.Call); ok {
				call = current
			}
			return true
		})
	}
	if call == nil || len(call.Args) != 1 {
		t.Fatalf("interface call = %#v", call)
	}
	fn, ok := call.Callee.ExprType().(*typeinfo.FuncType)
	if !ok || len(fn.Params) != 1 {
		t.Fatalf("callee type = %s, want one interface parameter", typeinfo.TypeText(call.Callee.ExprType()))
	}
	ctx := exprlower.Context{Types: ir.NewTypeTable(), Diagnostics: diag, Source: mod.THIR, ModuleID: mod.ID}
	lowered, ok := exprlower.Lower(ctx, call.Args[0], fn.Params[0]).(*ir.InterfaceMake)
	if !ok || len(lowered.Slots) != 1 {
		t.Fatalf("interface conversion = %#v, want one implementation slot", lowered)
	}
	if lowered.Slots[0].MethodName != "sum" || lowered.Slots[0].FuncName == "" || lowered.Slots[0].SlotType == ir.InvalidType {
		t.Fatalf("published interface slot = %#v", lowered.Slots[0])
	}
	missing := &thir.NumberLiteral{ExprInfo: thir.ExprInfo{Source: ir.SourceInfo{NodeID: 10}, Type: &typeinfo.IntegerType{IsSigned: true, Bits: 32}}, Value: "1"}
	invalid, ok := exprlower.Lower(ctx, missing, fn.Params[0]).(*ir.InvalidExpr)
	if !ok || invalid.Message != "missing interface implementation evidence" {
		t.Fatalf("conversion without evidence = %#v, want missing evidence", invalid)
	}
}
