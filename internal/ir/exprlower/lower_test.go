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
	"compiler/internal/semantics/typechecker"
	"compiler/pkg/peeper"
)

func TestLowerVariantConvertsPayloadToExpectedType(t *testing.T) {
	const source = `
enum Wide { Value: i64 }
fn make() -> Wide { return Wide::Value with 7i32; }
`
	const filePath = "variant_payload_conversion.peep"
	diag := diagnostics.NewDiagnosticBag()
	ctx := project.New(".", peeper.SourceExt, diag)
	mod := &module.Module{
		ID:              moduleid.ID{Origin: string(project.ModuleOriginLocal), ImportPath: "variant_payload_conversion"},
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
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	mod.THIR = thir.Build(mod.ID.ImportPath, mod.FilePath, mod.AST, mod.Bindings, mod.Typechecking)
	if err := mod.THIR.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

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
