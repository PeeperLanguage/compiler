package parser

import (
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
)

func TestParseEnumVariantDataSchemas(t *testing.T) {
	mod, diag := parseTestModule(`enum Result<T> {
	Ok: { value: T, },
	Error: { message: str, },
	Pending,
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	enumDecl := mod.Stmts[0].(*ast.EnumDecl)
	enumType := enumDecl.Type.(*ast.EnumType)
	if len(enumType.Variants) != 3 {
		t.Fatalf("variants = %d, want 3", len(enumType.Variants))
	}
	ok := enumType.Variants[0]
	if !ok.HasData || len(ok.Fields) != 1 || ok.Fields[0].Name.Name != "value" || ast.TypeText(ok.Fields[0].Type) != "T" {
		t.Fatalf("Ok schema = %#v", ok)
	}
	if enumType.Variants[2].HasData || len(enumType.Variants[2].Fields) != 0 {
		t.Fatalf("Pending should be payloadless: %#v", enumType.Variants[2])
	}
	if got := ast.TypeText(enumType); got != "enum {Ok: {value: T}, Error: {message: str}, Pending}" {
		t.Fatalf("enum text = %q", got)
	}
}

func TestEnumVariantDataSchemaChangesExportFingerprint(t *testing.T) {
	valueI32, diag := parseTestModule(`enum Result { Ok: { value: i32, }, Pending, }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	valueBool, diag := parseTestModule(`enum Result { Ok: { value: bool, }, Pending, }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	itemI32, diag := parseTestModule(`enum Result { Ok: { item: i32, }, Pending, }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if valueI32.ExportFingerprint == valueBool.ExportFingerprint {
		t.Fatal("field type change should alter export fingerprint")
	}
	if valueI32.ExportFingerprint == itemI32.ExportFingerprint {
		t.Fatal("field name change should alter export fingerprint")
	}
	payloadless, diag := parseTestModule(`enum Result { Pending, }`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	if got := payloadless.Stmts[0].(*ast.EnumDecl).GetDeclSurface(); got != "enum:Result:Pending" {
		t.Fatalf("payloadless surface = %q", got)
	}
}

func TestParseRejectsEmptyEnumVariantDataSchema(t *testing.T) {
	_, diag := parseTestModule(`enum Result { Ok: {}, Pending, }`)
	if !diag.HasErrors() {
		t.Fatal("expected empty data schema diagnostic")
	}
	if output := diag.EmitAllToString(); !strings.Contains(output, "variant data requires at least one field") {
		t.Fatalf("unexpected diagnostics:\n%s", output)
	}
}

func TestParseVariantLiteralsAcrossExpressionContexts(t *testing.T) {
	mod, diag := parseTestModule(`fn accept(value: Result<i32>) {}
fn make() -> Result<i32> {
	let direct = Result<i32>::Ok{ value = 1, };
	accept(pkg::Result<i32>::Ok{ value = 2 });
	let nested = [1]Result<i32>{Result<i32>::Ok{ value = 3 }};
	return Result<i32>::Ok{ value = direct.value };
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	body := mod.Stmts[1].(*ast.FnDecl).Body
	direct := body.Stmts[0].(*ast.LetDecl).Value.(*ast.VariantLit)
	if ast.ExprText(direct.Case) != "Result<i32>::Ok" || len(direct.Fields) != 1 {
		t.Fatalf("direct literal = %#v", direct)
	}
	call := body.Stmts[1].(*ast.ExprStmt).Expr.(*ast.CallExpr)
	if got := ast.ExprText(call.Args[0]); got != "pkg::Result<i32>::Ok{value = 2}" {
		t.Fatalf("call literal text = %q", got)
	}
	array := body.Stmts[2].(*ast.LetDecl).Value.(*ast.ArrayLit)
	if _, ok := array.Values[0].(*ast.VariantLit); !ok {
		t.Fatalf("array value = %T, want variant literal", array.Values[0])
	}
	ret := body.Stmts[3].(*ast.ReturnStmt).Value
	if _, ok := ret.(*ast.VariantLit); !ok {
		t.Fatalf("return value = %T, want variant literal", ret)
	}
}

func TestParseIsAtEqualityPrecedence(t *testing.T) {
	mod, diag := parseTestModule(`fn check(result: Result<i32>, ready: bool) -> bool {
	return result is Result<i32>::Ok && ready;
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	ret := mod.Stmts[0].(*ast.FnDecl).Body.Stmts[0].(*ast.ReturnStmt)
	logical := ret.Value.(*ast.BinaryExpr)
	test, ok := logical.Left.(*ast.IsExpr)
	if !ok || ast.ExprText(test.Value) != "result" || ast.ExprText(test.Case) != "Result<i32>::Ok" {
		t.Fatalf("left expression = %#v", logical.Left)
	}
}

func TestParseStatementMatch(t *testing.T) {
	mod, diag := parseTestModule(`fn inspect(result: Result<i32>) {
	match result {
		Result<i32>::Ok{ value = payload } => { println(payload); }
		Result<i32>::Error{ message = _, code = code } => { println(code); }
		Result<i32>::Pending => { println("wait"); }
	}
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	match := mod.Stmts[0].(*ast.FnDecl).Body.Stmts[0].(*ast.MatchStmt)
	if ast.ExprText(match.Subject) != "result" || len(match.Arms) != 3 {
		t.Fatalf("match = %#v", match)
	}
	if match.ArmListLocation == nil || match.ArmListLocation.Start == nil || match.ArmListLocation.End == nil ||
		match.ArmListLocation.Start.Index >= ast.StartOf(match.Arms[0]).Index ||
		match.ArmListLocation.End.Index <= ast.EndOf(match.Arms[len(match.Arms)-1]).Index {
		t.Fatalf("match arm-list location = %#v", match.ArmListLocation)
	}
	ok := match.Arms[0]
	if !ok.HasData || len(ok.Fields) != 1 || ok.Fields[0].Binding.Name != "payload" {
		t.Fatalf("Ok arm = %#v", ok)
	}
	errorArm := match.Arms[1]
	if !errorArm.Fields[0].Discard || errorArm.Fields[0].Binding != nil || errorArm.Fields[1].Binding.Name != "code" {
		t.Fatalf("Error fields = %#v", errorArm.Fields)
	}
	if match.Arms[2].HasData {
		t.Fatalf("Pending arm should be payloadless: %#v", match.Arms[2])
	}
}

func TestParseParenthesizedVariantLiteralInIfHeader(t *testing.T) {
	mod, diag := parseTestModule(`fn check(value: Result<i32>) {
	if value == (Result<i32>::Ok{ value = 1 }) { println("ok"); }
	if (value == Result<i32>::Ok{ value = 2 }) { println("ok"); }
}`)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	body := mod.Stmts[0].(*ast.FnDecl).Body
	for index, stmt := range body.Stmts {
		ifStmt := stmt.(*ast.IfStmt)
		comparison := ifStmt.Cond.(*ast.BinaryExpr)
		if _, ok := comparison.Right.(*ast.VariantLit); !ok {
			t.Fatalf("condition %d right = %T, want variant literal", index, comparison.Right)
		}
	}
}

func TestParseDiagnosesUnparenthesizedVariantLiteralInIfHeader(t *testing.T) {
	mod, diag := parseTestModule(`fn check(value: Result<i32>) {
	if value == Result<i32>::Ok{ value = 1 } { println("ok"); }
	if pkg::ready { println("ready"); }
}`)
	if !diag.HasErrors() {
		t.Fatal("expected ambiguous control-header diagnostic")
	}
	if output := diag.EmitAllToString(); !strings.Contains(output, "parenthesize variant literal in control header") {
		t.Fatalf("unexpected diagnostics:\n%s", output)
	}
	body := mod.Stmts[0].(*ast.FnDecl).Body
	if len(body.Stmts) != 2 {
		t.Fatalf("statements = %d, want recovery to retain both if statements", len(body.Stmts))
	}
	ordinary := body.Stmts[1].(*ast.IfStmt)
	if got := ast.ExprText(ordinary.Cond); got != "pkg::ready" {
		t.Fatalf("ordinary qualified condition = %q", got)
	}
}
