package parser

import (
	"strings"
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
)

func TestParseModuleSubset(t *testing.T) {
	src := `import "math" as m;
const x: i32 = 1 + 2 * 3;
let y: i32 = x;
fn add(a: i32, b: i32): i32 {
	let z: i32 = a + b;
	return z;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics")
	}
	if len(mod.Imports) != 1 {
		t.Fatalf("imports: got %d want 1", len(mod.Imports))
	}
	if len(mod.Decls) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Decls))
	}
	if _, ok := mod.Decls[0].(*ast.ConstDecl); !ok {
		t.Fatalf("decl[0] expected const")
	}
	if _, ok := mod.Decls[1].(*ast.LetDecl); !ok {
		t.Fatalf("decl[1] expected let")
	}
	fn, ok := mod.Decls[2].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[2] expected fn")
	}
	if fn.Name == nil || fn.Name.Name != "add" {
		t.Fatalf("fn name mismatch")
	}
	if len(fn.Params) != 2 {
		t.Fatalf("params: got %d want 2", len(fn.Params))
	}
	if fn.ReturnType == nil {
		t.Fatalf("missing return type")
	}
	if fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("fn body stmts mismatch")
	}
}

func TestParseRejectsNonI32Type(t *testing.T) {
	src := `let x: i64 = 1;`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	_ = ParseModule("test.em", stream, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected parser diagnostics")
	}
}

func TestParseLetWithoutExplicitType(t *testing.T) {
	src := `let c = a + b;`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics")
	}
	if len(mod.Decls) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Decls))
	}
	letDecl, ok := mod.Decls[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("decl[0] expected let")
	}
	if letDecl.Type != nil {
		t.Fatalf("let type should be nil when omitted")
	}
}

func TestParseFunctionWithReceiverAndTypeParams(t *testing.T) {
	src := `fn (r: i32) add[T, U](a: i32): i32 {
	return a;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics")
	}
	if len(mod.Decls) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Decls))
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	if fn.Receiver == nil || fn.Receiver.Name == nil || fn.Receiver.Name.Name != "r" {
		t.Fatalf("receiver not parsed")
	}
	if len(fn.TypeParams) != 2 {
		t.Fatalf("type params: got %d want 2", len(fn.TypeParams))
	}
	if fn.TypeParams[0].Name == nil || fn.TypeParams[0].Name.Name != "T" {
		t.Fatalf("first type param mismatch")
	}
	if fn.TypeParams[1].Name == nil || fn.TypeParams[1].Name.Name != "U" {
		t.Fatalf("second type param mismatch")
	}
}

func TestParseRecoversMissingSemicolonAndContinuesTopLevel(t *testing.T) {
	src := `let a: i32 = 10
let b: i32 = 23;
fn main(): i32 {
	return b;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for missing semicolon")
	}
	if len(mod.Decls) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Decls))
	}
	if _, ok := mod.Decls[2].(*ast.FnDecl); !ok {
		t.Fatalf("decl[2] expected fn")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "expected ';' after statement") {
		t.Fatalf("expected targeted missing-semicolon diagnostic, got:\n%s", out)
	}
}

func TestParseRecoversMissingParenInCallAndContinues(t *testing.T) {
	src := `fn main(): i32 {
	foo(1, 2;
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for missing ')'")
	}
	if len(mod.Decls) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Decls))
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	if fn.Body == nil || len(fn.Body.Stmts) == 0 {
		t.Fatalf("expected function body statements after recovery")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "expected ')' after arguments") {
		t.Fatalf("expected missing-paren diagnostic, got:\n%s", out)
	}
}

func TestParseRecoversMissingFunctionBlockAndContinues(t *testing.T) {
	src := `fn myfunc()
fn main(): i32 {
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for missing function block")
	}
	if len(mod.Decls) != 2 {
		t.Fatalf("decls: got %d want 2", len(mod.Decls))
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "missing function body") {
		t.Fatalf("expected missing-block diagnostic, got:\n%s", out)
	}
}

func TestParseMissingFunctionBlockDoesNotBreakNextFunction(t *testing.T) {
	src := `

fn myfunc()

fn main() {
	let a : i32 = 10;
	let b : i32 = 23;
	let c = a + b;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if len(mod.Decls) != 2 {
		t.Fatalf("decls: got %d want 2", len(mod.Decls))
	}
	out := diag.EmitAllToString()
	if strings.Contains(out, " --> test.em:5:1") && strings.Contains(out, "expected '{'") {
		t.Fatalf("unexpected extra missing-block diagnostic on second function:\n%s", out)
	}
}

func TestParseAllowsExternalFunctionSemicolon(t *testing.T) {
	src := `fn ext();
fn main(): i32 {
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Decls) != 2 {
		t.Fatalf("decls: got %d want 2", len(mod.Decls))
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	if fn.Body != nil {
		t.Fatalf("external fn body must be nil")
	}
}

func TestParseAllowsFunctionAttributes(t *testing.T) {
	src := `#[extern]
fn ext();
fn main(): i32 {
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Decls) != 2 {
		t.Fatalf("decls: got %d want 2", len(mod.Decls))
	}
}

func TestParseMethodStyleFunctionNamePath(t *testing.T) {
	src := `fn User::Method(self: i32, other: i32): i32 {
	return other;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Decls) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Decls))
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	if fn.Name == nil || fn.Name.Name != "User::Method" {
		t.Fatalf("method name mismatch: got %#v", fn.Name)
	}
}
