package parser

import (
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
)

func TestParseDestructureStmt(t *testing.T) {
	mod, diag := parseTestModule(`fn main() { let .{ first as a, second as b, _ } = make(); let .{ x as c } = point; }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	body := mod.Stmts[0].(*ast.FnDecl).Body.Stmts
	withRest := body[0].(*ast.DestructureStmt)
	if !withRest.HasRest || len(withRest.Fields) != 2 ||
		withRest.Fields[0].Name.Name != "first" || withRest.Fields[0].Binding.Name != "a" ||
		withRest.Fields[1].Name.Name != "second" || withRest.Fields[1].Binding.Name != "b" {
		t.Fatalf("destructure with rest = %#v", withRest)
	}
	if _, isCall := withRest.Value.(*ast.CallExpr); !isCall {
		t.Fatalf("destructure value = %T, want call", withRest.Value)
	}
	exact := body[1].(*ast.DestructureStmt)
	if exact.HasRest || len(exact.Fields) != 1 || exact.Fields[0].Binding.Name != "c" {
		t.Fatalf("exact destructure = %#v", exact)
	}
}

func TestParseDestructureRejectsMisplacedRest(t *testing.T) {
	_, diag := parseTestModule(`fn main() { let .{ _, x as a } = point; }`)
	if out := diag.EmitAllToString(); !strings.Contains(out, "`_` must be the last destructure item") {
		t.Fatalf("expected misplaced rest diagnostic, got:\n%s", out)
	}
}
