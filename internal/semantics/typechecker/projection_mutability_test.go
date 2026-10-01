package typechecker

import (
	"fmt"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
)

func TestOwnedProjectionMutation(t *testing.T) {
	const declarations = `struct Leaf { value: i32 }
struct Inner { value: i32, values: [2]i32, leaf: Leaf }
struct Outer { child: *Inner, children: [2]*Inner }
fn (self: &mut Leaf) bump() { self.value = 1; }
fn Update(value: &mut i32) { }
fn Raw(value: rawptr) { }
`
	for _, root := range []struct {
		name           string
		param          string
		shared         bool
		mutable        bool
		requireMutable bool
	}{
		{name: "shared", param: "outer: &Outer", shared: true},
		{name: "mutable shared binding", param: "mut outer: &Outer", shared: true},
		{name: "mutable reference", param: "outer: &mut Outer", mutable: true},
		{name: "mutable reference binding", param: "mut outer: &mut Outer", mutable: true},
		{name: "immutable owner", param: "outer: *Outer"},
		{name: "mutable owner binding", param: "mut outer: *Outer", mutable: true, requireMutable: true},
		{name: "immutable value", param: "outer: Outer"},
		{name: "mutable value", param: "mut outer: Outer", mutable: true, requireMutable: true},
	} {
		for _, operation := range []struct {
			name    string
			body    string
			code    string
			message string
			detail  string
		}{
			{name: "field assignment", body: "outer.child.value = 1;", code: diagnostics.ErrInvalidAssignment, message: "cannot assign through immutable reference", detail: "use `&mut Outer`"},
			{name: "mutable field borrow", body: "Update(&mut outer.child.value);", code: diagnostics.ErrInvalidExpression, message: "mutable reference requires mutable addressable storage", detail: "value is behind an immutable reference"},
			{name: "indexed owner field assignment", body: "outer.children[0].value = 1;", code: diagnostics.ErrInvalidAssignment, message: "cannot assign through immutable reference", detail: "use `&mut Outer`"},
			{name: "indexed owner mutable borrow", body: "Update(&mut outer.children[0].value);", code: diagnostics.ErrInvalidExpression, message: "mutable reference requires mutable addressable storage", detail: "value is behind an immutable reference"},
			{name: "array assignment", body: "outer.child.values[0] = 1;", code: diagnostics.ErrInvalidAssignment, message: "index assignment requires mutable array or slice binding"},
			{name: "array mutable borrow", body: "Update(&mut outer.child.values[0]);", code: diagnostics.ErrInvalidExpression, message: "mutable reference requires mutable addressable storage", detail: "value is behind an immutable reference"},
			{name: "slice assignment", body: "let view = outer.child.values[..]; view[0] = 1;", code: diagnostics.ErrInvalidAssignment, message: "index assignment requires mutable array or mutable slice view"},
			{name: "mutable receiver", body: "outer.child.leaf.bump();", code: diagnostics.ErrInvalidAssignment, message: "implicit mutable borrow requires a mutable binding"},
			{name: "mutable pipe", body: "outer.child.value |> Update();", code: diagnostics.ErrInvalidAssignment, message: "implicit mutable borrow requires a mutable binding"},
			{name: "indexed mutable receiver", body: "outer.children[0].leaf.bump();", code: diagnostics.ErrTypeMismatch, message: "cannot implicitly convert"},
			{name: "indexed mutable pipe", body: "outer.child.values[0] |> Update();", code: diagnostics.ErrTypeMismatch, message: "cannot implicitly convert"},
			{name: "owner mutable borrow", body: "let view = &mut outer.child;", code: diagnostics.ErrInvalidExpression, message: "mutable reference requires mutable addressable storage", detail: "value is behind an immutable reference"},
			{name: "raw address", body: "Raw(@outer.child.value);"},
		} {
			t.Run(root.name+"/"+operation.name, func(t *testing.T) {
				mod, diag := checkTypeModule(t, declarations+fmt.Sprintf("fn Check(%s) { %s }", root.param, operation.body))
				if root.mutable || operation.code == "" {
					if diag.HasErrors() {
						t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
					}
				} else {
					code, message, detail := operation.code, operation.message, operation.detail
					if !root.shared {
						detail = ""
						if message == "cannot assign through immutable reference" {
							message = "field assignment requires a mutable pointer, reference, or local binding"
						}
					}
					// Mutable bindings do not get the immutable-binding receiver diagnostic.
					if root.name == "mutable shared binding" && message == "implicit mutable borrow requires a mutable binding" {
						code, message = diagnostics.ErrTypeMismatch, "cannot implicitly convert"
					}
					if out := diag.EmitAllToString(); !hasTypeCode(diag, code) || !strings.Contains(out, message) || !strings.Contains(out, detail) {
						t.Fatalf("expected %s: %q, detail %q; got:\n%s", code, message, detail, out)
					}
				}
				found := false
				ast.Inspect(mod.AST.Stmts[len(mod.AST.Stmts)-1], func(node ast.Node) bool {
					if ident, ok := node.(*ast.Ident); ok && ident.Name == "outer" {
						if sym := mod.SymbolIndex.Symbol(ident); sym != nil {
							found = true
							wantMutable := root.requireMutable && operation.code != ""
							if got := mod.SymbolIndex.RequiresMutable(sym); got != wantMutable {
								t.Errorf("projection requires mutable outer binding = %v, want %v", got, wantMutable)
							}
						}
					}
					return true
				})
				if !found {
					t.Fatal("missing outer binding")
				}
			})
		}
	}
}

func TestOwnedPointerBindingMutation(t *testing.T) {
	for _, root := range []struct {
		name    string
		header  string
		mutable bool
	}{
		{name: "immutable parameter", header: "fn Check(owner: *Box) {"},
		{name: "mutable parameter", header: "fn Check(mut owner: *Box) {", mutable: true},
		{name: "immutable local", header: "fn Check() { let owner = alloc(Box.{value = 0});"},
		{name: "mutable local", header: "fn Check() { let mut owner = alloc(Box.{value = 0});", mutable: true},
	} {
		for _, operation := range []struct {
			name string
			body string
			code string
		}{
			{name: "assignment", body: "owner.value = 1;", code: diagnostics.ErrInvalidAssignment},
			{name: "field borrow", body: "Update(&mut owner.value);", code: diagnostics.ErrInvalidExpression},
			{name: "owner borrow", body: "let view = &mut owner;", code: diagnostics.ErrInvalidExpression},
			{name: "pipe borrow", body: "owner.value |> Update();", code: diagnostics.ErrInvalidAssignment},
			{name: "shared borrow", body: "let view = &owner.value;"},
			{name: "raw address", body: "let address = @owner.value;"},
		} {
			t.Run(root.name+"/"+operation.name, func(t *testing.T) {
				diag := checkTypeSource(t, `struct Box { value: i32 }
fn Update(value: &mut i32) { }
`+root.header+operation.body+"}")
				if root.mutable || operation.code == "" {
					if diag.HasErrors() {
						t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
					}
				} else if !hasTypeCode(diag, operation.code) {
					t.Fatalf("expected %s, got:\n%s", operation.code, diag.EmitAllToString())
				}
			})
		}
	}
}

func TestProjectionMutationRequiresMutableBinding(t *testing.T) {
	for _, body := range []string{
		"outer.leaf.value = 1;",
		"Update(&mut outer.leaf.value);",
		"outer.values[0] = 1;",
		"Update(&mut outer.values[0]);",
		"let view = outer.values[..]; view[0] = 1;",
		"outer.leaf.bump();",
	} {
		t.Run(body, func(t *testing.T) {
			mod, diag := checkTypeModule(t, `struct Leaf { value: i32 }
struct Outer { leaf: Leaf, values: [2]i32 }
fn (self: &mut Leaf) bump() { self.value = 1; }
fn Update(value: &mut i32) { }
fn Check(mut outer: Outer) { `+body+` }`)
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
			}
			found := false
			ast.Inspect(mod.AST.Stmts[len(mod.AST.Stmts)-1], func(node ast.Node) bool {
				if ident, ok := node.(*ast.Ident); ok && ident.Name == "outer" {
					if sym := mod.SymbolIndex.Symbol(ident); sym != nil {
						found = true
						if !mod.SymbolIndex.RequiresMutable(sym) {
							t.Error("projection failed to require mutable outer binding")
						}
					}
				}
				return true
			})
			if !found {
				t.Fatal("missing outer binding")
			}
		})
	}
}
