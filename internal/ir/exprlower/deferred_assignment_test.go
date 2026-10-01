package exprlower_test

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/exprlower"
	"compiler/internal/ir/mir"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/typeinfo"
)

func TestDeferredEnumAssignmentConversions(t *testing.T) {
	for _, test := range []struct {
		name, target, body string
		kind               typeinfo.ConversionKind
	}{
		{
			name: "integer widening", target: "i64", kind: typeinfo.ConversionNumeric,
			body: `fn Replace(mut value: Value, replacement: i32) { if value is Value::Integer { value.item = replacement; } }`,
		},
		{
			name: "literal widening", target: "i64", kind: typeinfo.ConversionNumeric,
			body: `fn Replace(mut value: Value) { if value is Value::Integer { value.item = 7; } }`,
		},
		{
			name: "float widening", target: "f64", kind: typeinfo.ConversionNumeric,
			body: `fn Replace(mut value: Value, replacement: f32) { if value is Value::Integer { value.item = replacement; } }`,
		},
		{
			name: "optional injection", target: "?i32", kind: typeinfo.ConversionOptional,
			body: `fn Replace(mut value: Value, replacement: i32) { if value is Value::Integer { value.item = replacement; } }`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mod, diag := buildTypedExprModule(t, "enum Value { Integer: { item: "+test.target+" }, Flag: { item: bool } }\n"+test.body)
			var assignment *thir.Assign
			thir.Inspect(mod.THIR.Functions[0].Body, func(node thir.Node) bool {
				if assign, ok := node.(*thir.Assign); ok {
					assignment = assign
				}
				return true
			})
			if assignment == nil {
				t.Fatal("missing assignment")
			}
			conversion, ok := mod.Analysis.ImplicitConversion(assignment.Value.SourceInfo().NodeID)
			if !ok || conversion.Kind != test.kind || conversion.Compatibility != typeinfo.Compatible {
				t.Fatalf("late conversion = %#v, %v", conversion, ok)
			}
			if assignment.Value.Conversion() != nil {
				t.Fatal("flow mutated base conversion evidence")
			}
			types := ir.NewTypeTable()
			target := mod.EffectiveExprType(assignment.Target.SourceInfo().NodeID)
			lowered := exprlower.Lower(exprlower.Context{Types: types, Diagnostics: diag, Source: mod.THIR, Analysis: mod.Analysis, ModuleID: mod.ID}, assignment.Value, target)
			if got := types.Text(lowered.TypeID()); got != test.target {
				t.Fatalf("lowered RHS type = %s, want %s", got, test.target)
			}
			if test.kind == typeinfo.ConversionNumeric {
				if cast, ok := lowered.(*ir.Cast); !ok || cast.TypeID() == cast.Expr.TypeID() {
					t.Fatalf("lowered RHS = %#v, want widening cast", lowered)
				}
			} else if _, ok := lowered.(*ir.VariantMake); !ok {
				t.Fatalf("lowered RHS = %T, want optional construction", lowered)
			}
			out := mir.GenerateMIR(mir.LoweringInput{
				Types: types, Diagnostics: diag, Source: mod.THIR, CFG: mod.CFG, Analysis: mod.Analysis,
				Scope: mod.ModuleScope, SymbolIndex: mod.SymbolIndex, ModuleID: mod.ID,
			})
			if out == nil || diag.HasErrors() {
				t.Fatalf("MIR lowering failed:\n%s", diag.EmitAllToString())
			}
			if err := out.Validate(); err != nil {
				t.Fatalf("invalid MIR: %v", err)
			}
		})
	}
}
