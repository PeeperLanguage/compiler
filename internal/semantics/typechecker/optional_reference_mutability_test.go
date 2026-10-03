package typechecker

import (
	"fmt"
	"testing"

	"compiler/internal/diagnostics"
)

func TestOptionalReferenceProjectionMutability(t *testing.T) {
	const declarations = `struct Leaf { value: i32 }
struct Box { value: i32, values: [2]i32, leaf: Leaf }
fn (self: &mut Leaf) bump() { self.value = 9; }
fn Update(value: &mut i32) { }
`
	for _, root := range []struct {
		name    string
		param   string
		mutable bool
	}{
		{name: "shared", param: "x: ?&Box"},
		{name: "mutable shared binding", param: "mut x: ?&Box"},
		{name: "mutable reference", param: "x: ?&mut Box", mutable: true},
		{name: "mutable reference binding", param: "mut x: ?&mut Box", mutable: true},
	} {
		for _, operation := range []struct {
			name string
			body string
		}{
			{name: "field assignment", body: "x.value = 9;"},
			{name: "field borrow", body: "Update(&mut x.value);"},
			{name: "index assignment", body: "x.values[0] = 9;"},
			{name: "index borrow", body: "Update(&mut x.values[0]);"},
			{name: "slice assignment", body: "let view = x.values[..]; view[0] = 9;"},
			{name: "receiver", body: "x.leaf.bump();"},
			{name: "pipe", body: "x.value |> Update();"},
		} {
			t.Run(root.name+"/"+operation.name, func(t *testing.T) {
				diag := checkTypeSource(t, declarations+fmt.Sprintf("fn Check(%s) { if x != none { %s } }", root.param, operation.body))
				if root.mutable {
					if diag.HasErrors() {
						t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
					}
				} else if !hasTypeCode(diag, diagnostics.ErrInvalidAssignment) && !hasTypeCode(diag, diagnostics.ErrInvalidExpression) && !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
					t.Fatalf("expected mutation rejection, got:\n%s", diag.EmitAllToString())
				}
			})
		}
	}
}
