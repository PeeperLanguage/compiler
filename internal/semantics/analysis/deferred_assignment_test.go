package analysis_test

import (
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/ir/thir"
	"compiler/internal/semantics/typeinfo"
)

func TestDeferredEnumFieldAssignment(t *testing.T) {
	const declaration = `enum Value { Integer: { item: i32 }, Flag: { item: bool } }
`
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "immutable target",
			body: `fn Corrupt(value: Value, replacement: i32) { if value is Value::Integer { value.item = replacement; } }`,
			want: "field assignment requires a mutable pointer, reference, or local binding",
		},
		{
			name: "mutable integer target",
			body: `fn Replace(mut value: Value, replacement: i32) { if value is Value::Integer { value.item = replacement; } }`,
		},
		{
			name: "mutable flag target",
			body: `fn Replace(mut value: Value, replacement: bool) { if value is Value::Flag { value.item = replacement; } }`,
		},
		{
			name: "wrong integer payload type",
			body: `fn Replace(mut value: Value, replacement: bool) { if value is Value::Integer { value.item = replacement; } }`,
			want: "cannot assign bool to i32",
		},
		{
			name: "wrong flag payload type",
			body: `fn Replace(mut value: Value, replacement: i32) { if value is Value::Flag { value.item = replacement; } }`,
			want: "cannot assign i32 to bool",
		},
		{
			name: "typed integer literal",
			body: `fn Replace(mut value: Value) { if value is Value::Integer { value.item = 7; } }`,
		},
		{
			name: "invalid RHS operation",
			body: `fn Replace(mut value: Value) { if value is Value::Integer { value.item = true + false; } }`,
			want: "operator",
		},
		{
			name: "missing case proof",
			body: `fn Replace(mut value: Value, replacement: i32) { value.item = replacement; }`,
			want: "unknown member `item`",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mod, diag := checkAnalysisSource(t, declaration+test.body)
			if test.want != "" {
				if !diag.HasErrors() || !strings.Contains(diag.EmitAllToString(), test.want) {
					t.Fatalf("expected %q, got:\n%s", test.want, diag.EmitAllToString())
				}
				return
			}
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
			fn := mod.AST.Stmts[1].(*ast.FnDecl)
			assignment := fn.Body.Stmts[0].(*ast.IfStmt).Then.Stmts[0].(*ast.AssignStmt)
			rhs, ok := mod.THIR.Node(assignment.Value.ID()).(thir.Expr)
			if !ok || rhs.ExprType() == nil || typeinfo.IsInvalidOrUnknown(rhs.ExprType()) {
				t.Fatalf("RHS lacks base type: %#v", rhs)
			}
			if got := mod.EffectiveExprType(assignment.Target.ID()); !typeinfo.IsSameType(got, rhs.ExprType()) {
				t.Fatalf("target type = %s, RHS type = %s", typeinfo.TypeText(got), typeinfo.TypeText(rhs.ExprType()))
			}
		})
	}
}
