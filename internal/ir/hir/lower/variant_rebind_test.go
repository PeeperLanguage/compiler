package lower

import (
	"testing"

	"compiler/internal/ir"
	"compiler/internal/ir/hir"
	"compiler/pkg/peeper"
)

func TestGenerateHIRLowersIndexAssignment(t *testing.T) {
	out := generateTestHIR(t, "hir_index_assignment"+peeper.SourceExt, "hir_index_assignment", `fn main() {
	let mut values = [2]i32{1, 2};
	values[0] = 7;
}`)
	assign := out.Funcs[0].Body.Stmts[1].(*hir.Assign)
	projections := assign.Target.Projections
	if len(projections) != 1 || projections[0].Kind != ir.PlaceProjectionIndex {
		t.Fatalf("index assignment projections = %#v, want index", projections)
	}
	if out.Types.Text(assign.Target.Type) != "i32" || assign.Value.TypeID() != assign.Target.Type {
		t.Fatal("index assignment lost element type")
	}
}

func TestGenerateHIRLowersVariantReferenceFieldRebind(t *testing.T) {
	out := generateTestHIR(t, "hir_variant_rebind_test"+peeper.SourceExt, "hir_variant_rebind_test", `enum Resource { Borrowed: { value: &i32 }, Empty }
fn Read(_: &i32) {}
fn probe(mut first: i32, mut second: i32) {
	let mut resource = Resource::Borrowed with .{ value = &first };
	if resource is Resource::Borrowed {
		resource.value = &second;
		match resource {
			Resource::Borrowed with { value = ref } => { Read(ref); }
			Resource::Empty => {}
		}
	}
}`)
	branch := out.Funcs[1].Body.Stmts[1].(*hir.If)
	assign := branch.Then.Stmts[0].(*hir.Assign)
	projections := assign.Target.Projections
	if len(projections) != 2 {
		t.Fatalf("rebind projections = %#v, want payload then field", projections)
	}
	if projections[0].Kind != ir.PlaceProjectionVariantPayload || projections[0].Case != 0 ||
		projections[1].Kind != ir.PlaceProjectionField || projections[1].FieldIndex != 0 {
		t.Fatalf("rebind projections = %#v, want Borrowed payload then value field", projections)
	}
	if out.Types.Text(assign.Target.Type) != "&i32" || assign.Value.TypeID() != assign.Target.Type {
		t.Fatalf("rebind types = %s <- %s, want &i32", out.Types.Text(assign.Target.Type), out.Types.Text(assign.Value.TypeID()))
	}
}
