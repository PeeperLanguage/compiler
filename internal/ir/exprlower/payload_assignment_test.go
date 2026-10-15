package exprlower_test

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/exprlower"
	"compiler/internal/ir/mir"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/types"
)

func TestEnumPayloadFieldAssignmentConversions(t *testing.T) {
	for _, test := range []struct {
		name, target, body string
		kind               types.ConversionKind
	}{
		{
			name: "integer widening", target: "i64", kind: types.ConversionNumeric,
			body: `fn Replace(mut value: Value, replacement: i32) { if value is Value::Integer { value.item = replacement; } }`,
		},
		{
			name: "float widening", target: "f64", kind: types.ConversionNumeric,
			body: `fn Replace(mut value: Value, replacement: f32) { if value is Value::Integer { value.item = replacement; } }`,
		},
		{
			name: "optional injection", target: "?i32", kind: types.ConversionOptional,
			body: `fn Replace(mut value: Value, replacement: i32) { if value is Value::Integer { value.item = replacement; } }`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mod, diag := buildTypedExprModule(t, "enum Value { Integer: struct { item: "+test.target+" }, Flag: struct { item: bool } }\n"+test.body)
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
			conversion := assignment.Value.Conversion()
			if conversion == nil || conversion.Kind != test.kind || conversion.Compatibility != types.Compatible {
				t.Fatalf("conversion = %#v", conversion)
			}
			typeTable := ir.NewTypeTable()
			target := mod.BaseExprType(assignment.Target.SourceInfo().NodeID)
			lowered := exprlower.Lower(exprlower.Context{Types: typeTable, Diagnostics: diag, Source: mod.THIR, ModuleID: mod.ID}, assignment.Value, target)
			if got := typeTable.Text(lowered.TypeID()); got != test.target {
				t.Fatalf("lowered RHS type = %s, want %s", got, test.target)
			}
			if test.kind == types.ConversionNumeric {
				if cast, ok := lowered.(*ir.Cast); !ok || cast.TypeID() == cast.Expr.TypeID() {
					t.Fatalf("lowered RHS = %#v, want widening cast", lowered)
				}
			} else if _, ok := lowered.(*ir.VariantMake); !ok {
				t.Fatalf("lowered RHS = %T, want optional construction", lowered)
			}
			out := mir.GenerateMIR(mir.LoweringInput{
				Types: typeTable, Diagnostics: diag, Source: mod.THIR, CFG: mod.CFG,
				Scope: mod.ModuleScope, SymbolIndex: mod.SymbolIndex, ModuleID: mod.ID,
			})
			if out == nil || diag.HasErrors() {
				t.Fatalf("MIR lowering failed:\n%s", diag.EmitAllToString())
			}
			mir.ResolveDrops(out, mod.Analysis)
			if err := out.Validate(); err != nil {
				t.Fatalf("invalid MIR: %v", err)
			}
		})
	}
}
