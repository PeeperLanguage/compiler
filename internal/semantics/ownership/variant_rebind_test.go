package ownership

import (
	"fmt"
	"slices"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/place"
)

func TestProjectedEnumReferenceState(t *testing.T) {
	result := checkOwnershipSource(t, `enum Resource { Borrowed: { value: &i32, sibling: &i32 }, Empty }
fn Read(_: &i32) {}
fn probe(mut first: i32, mut second: i32) {
	let reference = &first;
	let mut resource = Resource::Borrowed with .{ value = reference, sibling = reference };
	if resource is Resource::Borrowed {
		resource.value = &second;
		resource.value = resource.value;
		match resource {
			Resource::Borrowed with { value = value, sibling = sibling } => { Read(value); Read(sibling); }
			Resource::Empty => {}
		}
	}
}`)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
	}
	analysis := inspectFunctionAnalysis(t, result, "probe")
	fn := result.module.AST.Stmts[2].(*ast.FnDecl)
	branch := fn.Body.Stmts[2].(*ast.IfStmt)
	assign := branch.Then.Stmts[0].(*ast.AssignStmt)
	match := branch.Then.Stmts[2].(*ast.MatchStmt)
	node := analysisNodeForStmt(t, analysis, match)
	resource, _ := analysis.functionScope.Lookup("resource")
	first, _ := analysis.functionScope.Lookup("first")
	second, _ := analysis.functionScope.Lookup("second")
	value := analysis.inStates[node.cfgSite.ID].references[resource]
	if len(value) != 2 {
		t.Fatalf("carrier loans = %#v, want two distinct slots", value)
	}
	for _, loan := range value {
		if len(loan.path) != 2 || loan.path[0].Kind != place.OriginVariantPayload || loan.path[0].Case != 0 {
			t.Fatalf("invalid carrier path: %#v", loan.path)
		}
		want := first
		if loan.path[1].Field == "value" {
			want = second
			if loan.id.node != assign.Value {
				t.Fatal("field self-assignment changed loan identity")
			}
		} else if loan.path[1].Field != "sibling" {
			t.Fatalf("unexpected field path: %#v", loan.path)
		}
		if !place.SameOrigins(loan.origins, []place.Origin{{Root: want}}) {
			t.Fatalf("%s loan origins = %#v, want %s", loan.path[1].Field, loan.origins, want.Name)
		}
	}
	storage := result.module.Flow.ResolvedStorageOrigins[assign.Target.ID()]
	if len(storage) != 1 || storage[0].Root != resource || !slices.Equal(storage[0].Projections, []place.OriginProjection{
		{Kind: place.OriginVariantPayload, Case: 0}, {Kind: place.OriginField, Field: "value"},
	}) {
		t.Fatalf("assignment storage = %#v", storage)
	}
	binding := match.Arms[0].Fields[0].Binding
	if got := result.module.Flow.ResolvedValueOrigins[binding.ID()]; !place.SameOrigins(got, []place.Origin{{Root: second}}) {
		t.Fatalf("match value origins = %#v, want second", got)
	}
}

func TestProjectedEnumReferenceRebind(t *testing.T) {
	tests := []struct {
		name, fields, setup, body, code string
	}{
		{name: "new referent stays borrowed", body: `resource.value = &second;
second = 3;
Read(resource.value);`, code: diagnostics.ErrBorrowConflict},
		{name: "old referent released", body: `resource.value = &second;
first = 3;
Read(resource.value);`},
		{name: "sibling retains old loan", fields: "value: &i32, sibling: &i32", setup: `let mut resource = Resource::Borrowed with .{ value = &first, sibling = &first };`, body: `resource.value = &second;
first = 3;
Read(resource.sibling);`, code: diagnostics.ErrBorrowConflict},
		{name: "same loan in two fields survives one replacement", fields: "value: &i32, sibling: &i32", setup: `let reference = &first;
let mut resource = Resource::Borrowed with .{ value = reference, sibling = reference };`, body: `resource.value = &second;
first = 3;
Read(resource.sibling);`, code: diagnostics.ErrBorrowConflict},
		{name: "same loan released after both replacements", fields: "value: &i32, sibling: &i32", setup: `let reference = &first;
let mut resource = Resource::Borrowed with .{ value = reference, sibling = reference };`, body: `resource.value = &second;
resource.sibling = &second;
first = 3;
Read(resource.value);
Read(resource.sibling);`},
		{name: "carrier copy retains old loan", body: `let duplicate = resource;
resource.value = &second;
first = 3;
match duplicate {
Resource::Borrowed with { value = reference } => { Read(reference); }
Resource::Empty => {}
}`, code: diagnostics.ErrBorrowConflict},
		{name: "field copy retains old loan", body: `let duplicate = resource.value;
resource.value = &second;
first = 3;
Read(duplicate);`, code: diagnostics.ErrBorrowConflict},
		{name: "independent copies release independently", body: `let mut duplicate = resource;
resource.value = &second;
if duplicate is Resource::Borrowed {
duplicate.value = &second;
first = 3;
Read(duplicate.value);
Read(resource.value);
}`},
		{name: "self assignment retains loan", body: `resource.value = resource.value;
first = 3;
Read(resource.value);`, code: diagnostics.ErrBorrowConflict},
		{name: "self assignment then replacement releases loan", body: `resource.value = resource.value;
resource.value = &second;
first = 3;
Read(resource.value);`},
		{name: "optional clear releases loan", fields: "value: ?&i32", body: `resource.value = none;
first = 3;
ReadOptional(resource.value);`},
		{name: "optional clear preserves sibling loan", fields: "value: ?&i32, sibling: &i32", setup: `let reference = &first;
let mut resource = Resource::Borrowed with .{ value = reference, sibling = reference };`, body: `resource.value = none;
first = 3;
Read(resource.sibling);`, code: diagnostics.ErrBorrowConflict},
		{name: "mutable replacement releases old loan", fields: "value: &mut i32", setup: `let mut resource = Resource::Borrowed with .{ value = &mut first };`, body: `resource.value = &mut second;
first = 3;
Write(resource.value);`},
		{name: "mutable replacement protects new loan", fields: "value: &mut i32", setup: `let mut resource = Resource::Borrowed with .{ value = &mut first };`, body: `resource.value = &mut second;
second = 3;
match resource {
Resource::Borrowed with { value = reference } => { Write(reference); }
Resource::Empty => {}
}`, code: diagnostics.ErrBorrowConflict},
		{name: "mutable moved carrier retains new loan", fields: "value: &mut i32", setup: `let mut resource = Resource::Borrowed with .{ value = &mut first };`, body: `resource.value = &mut second;
let moved = resource;
second = 3;
match moved {
Resource::Borrowed with { value = reference } => { Write(reference); }
Resource::Empty => {}
}`, code: diagnostics.ErrBorrowConflict},
		{name: "both branches release old loan", body: `if flag { resource.value = &second; } else { resource.value = &third; }
first = 3;
Read(resource.value);`},
		{name: "one branch retains old loan", body: `if flag { resource.value = &second; }
first = 3;
Read(resource.value);`, code: diagnostics.ErrBorrowConflict},
		{name: "branch replacement protects new loan", body: `if flag { resource.value = &second; }
second = 3;
Read(resource.value);`, code: diagnostics.ErrBorrowConflict},
		{name: "zero iteration loop retains old loan", body: `for flag { resource.value = &second; }
first = 3;
Read(resource.value);`, code: diagnostics.ErrBorrowConflict},
		{name: "loop replacement protects new loan", body: `for flag { resource.value = &second; }
second = 3;
Read(resource.value);`, code: diagnostics.ErrBorrowConflict},
		{name: "post loop replacement releases old loan", body: `for flag { resource.value = &second; }
resource.value = &third;
first = 3;
second = 4;
Read(resource.value);`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.fields == "" {
				test.fields = "value: &i32"
			}
			if test.setup == "" {
				test.setup = "let mut resource = Resource::Borrowed with .{ value = &first };"
			}
			src := fmt.Sprintf(`enum Resource { Borrowed: { %s }, Empty }
fn Read(_: &i32) {}
fn ReadOptional(_: ?&i32) {}
fn Write(_: &mut i32) {}
fn probe(mut first: i32, mut second: i32, mut third: i32, flag: bool) {
%s
if resource is Resource::Borrowed {
%s
}
}`, test.fields, test.setup, test.body)
			result := checkOwnershipSource(t, src)
			if test.code == "" {
				if result.HasErrors() {
					t.Fatalf("unexpected diagnostics:\n%s", result.EmitAllToString())
				}
			} else if !hasOwnershipCode(result, test.code) {
				t.Fatalf("expected %s:\n%s", test.code, result.EmitAllToString())
			}
		})
	}
}
