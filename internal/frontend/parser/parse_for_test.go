package parser

import (
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
)

func parseForBody(t *testing.T, src string) *ast.ForStmt {
	t.Helper()
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("module stmts = %d, want 1", len(mod.Stmts))
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) == 0 {
		t.Fatalf("expected function with statements, got %#v", mod.Stmts)
	}
	forStmt, ok := fn.Body.Stmts[0].(*ast.ForStmt)
	if !ok {
		t.Fatalf("expected for stmt, got %#v", fn.Body.Stmts[0])
	}
	return forStmt
}

func TestParseForConditionForm(t *testing.T) {
	src := `fn main() -> i32 {
for x < 10 {
	return 1;
}
return 0;
}`
	forStmt := parseForBody(t, src)
	if forStmt.Value != nil || forStmt.Iterable != nil {
		t.Fatalf("expected condition form, got value=%v iterable=%v", forStmt.Value, forStmt.Iterable)
	}
	if forStmt.Cond == nil {
		t.Fatal("expected condition")
	}
}

func TestParseForInSingleBinding(t *testing.T) {
	src := `fn main() -> i32 {
for i in 0..10 {
	return 1;
}
return 0;
}`
	forStmt := parseForBody(t, src)
	if forStmt.Cond != nil {
		t.Fatalf("expected nil condition, got %#v", forStmt.Cond)
	}
	if forStmt.Value == nil || forStmt.Index != nil {
		t.Fatalf("expected single value binding, got index=%v value=%v", forStmt.Index, forStmt.Value)
	}
	if forStmt.Value.Name != "i" {
		t.Fatalf("binding value = %q, want i", forStmt.Value.Name)
	}
	if _, ok := forStmt.Iterable.(*ast.RangeExpr); !ok {
		t.Fatalf("expected range iterable, got %#v", forStmt.Iterable)
	}
}

func TestParseForInIndexValueBinding(t *testing.T) {
	src := `fn main() -> i32 {
for index, value in 0..10 {
	return 1;
}
return 0;
}`
	forStmt := parseForBody(t, src)
	if forStmt.Index == nil || forStmt.Value == nil {
		t.Fatalf("expected index and value bindings, got index=%v value=%v", forStmt.Index, forStmt.Value)
	}
	if forStmt.Index.Name != "index" || forStmt.Value.Name != "value" {
		t.Fatalf("binding names = %q, %q", forStmt.Index.Name, forStmt.Value.Name)
	}
}

func TestParseBreakContinue(t *testing.T) {
	src := `fn main() -> i32 {
for x < 10 {
	break;
	continue;
}
return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn := mod.Stmts[0].(*ast.FnDecl)
	forStmt := fn.Body.Stmts[0].(*ast.ForStmt)
	if len(forStmt.Body.Stmts) != 2 {
		t.Fatalf("body stmts = %d, want 2", len(forStmt.Body.Stmts))
	}
	if _, ok := forStmt.Body.Stmts[0].(*ast.BreakStmt); !ok {
		t.Fatalf("expected break stmt, got %#v", forStmt.Body.Stmts[0])
	}
	if _, ok := forStmt.Body.Stmts[1].(*ast.ContinueStmt); !ok {
		t.Fatalf("expected continue stmt, got %#v", forStmt.Body.Stmts[1])
	}
}

func TestParseForInInvalidBindingRegistersRecoveryNode(t *testing.T) {
	src := `fn main() -> i32 {
for 1 in 0..2 {}
return 0;
}`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for invalid loop binding")
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) == 0 {
		t.Fatalf("expected function with loop, got %#v", mod.Stmts)
	}
	loop, ok := fn.Body.Stmts[0].(*ast.ForStmt)
	if !ok || loop.Value == nil {
		t.Fatalf("expected recovered for-in binding, got %#v", fn.Body.Stmts[0])
	}
	if loop.Value.ID() == 0 {
		t.Fatal("recovery binding has unregistered node ID")
	}
}

func TestParseForCommaRequiresIn(t *testing.T) {
	src := `fn main() -> i32 {
for i, v {
	return 1;
}
return 0;
}`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for comma without 'in'")
	}
}

func TestParseMalformedForInHeaderPreservesLoopShape(t *testing.T) {
	for _, test := range []struct {
		name   string
		header string
	}{
		{name: "missing first binding", header: ", value in values"},
		{name: "missing second binding", header: "index, in values"},
		{name: "extra binding", header: "index, value, extra in values"},
		{name: "missing iterable", header: "value in"},
		{name: "malformed iterable", header: "value in +"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mod, diag := parseTestModule("fn main() { for " + test.header + " {} return; }")
			if !diag.HasErrors() {
				t.Fatal("expected malformed-header diagnostic")
			}
			if strings.Contains(diag.EmitAllToString(), "missing for body") {
				t.Fatalf("unexpected body-recovery cascade:\n%s", diag.EmitAllToString())
			}
			fn := mod.Stmts[0].(*ast.FnDecl)
			if len(fn.Body.Stmts) != 2 {
				t.Fatalf("function statements = %d, want recovered loop and return", len(fn.Body.Stmts))
			}
			loop, ok := fn.Body.Stmts[0].(*ast.ForStmt)
			if !ok || loop.Cond != nil || loop.Iterable == nil || loop.Body == nil {
				t.Fatalf("malformed header lost for-in shape: %#v", fn.Body.Stmts[0])
			}
			if loop.Value == nil || loop.Value.ID() == 0 {
				t.Fatalf("recovery value binding = %#v, want registered identifier", loop.Value)
			}
			if _, ok := fn.Body.Stmts[1].(*ast.ReturnStmt); !ok {
				t.Fatalf("following statement = %#v, want return outside loop", fn.Body.Stmts[1])
			}
		})
	}
}

func TestParseRejectsLabeledBreak(t *testing.T) {
	src := `fn main() -> i32 {
for x < 10 {
	break outer;
}
return 0;
}`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for labeled break")
	}
}
