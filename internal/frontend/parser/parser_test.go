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
fn add(a: i32, b: i32) -> i32 {
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

func TestParseAllowsBuiltinNumericTypes(t *testing.T) {
	src := `let x: i64 = 1;
let y: f32 = 2;
fn sum(a: i64, b: f32) -> f64 {
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected parser diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Decls) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Decls))
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

func TestParseFunctionWithTypeParams(t *testing.T) {
	src := `fn add[T, U](a: i32) -> i32 {
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

func TestParseFunctionReturnArrowSyntax(t *testing.T) {
	src := `fn main() -> i32 {
	return 10;
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
	ret, ok := fn.ReturnType.(*ast.NamedType)
	if !ok || ret.Name != "i32" {
		t.Fatalf("return type mismatch: %#v", fn.ReturnType)
	}
}

func TestParseUnaryPlus(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = +(10 as i32);
	return x;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("unexpected function body: %#v", mod.Decls[0])
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("expected let stmt, got %#v", fn.Body.Stmts[0])
	}
	unary, ok := letDecl.Value.(*ast.UnaryExpr)
	if !ok || unary.Op != "+" {
		t.Fatalf("expected unary plus, got %#v", letDecl.Value)
	}
}

func TestParseCastBindsLooserThanUnary(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i8 = -128 as i8;
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("unexpected function body: %#v", mod.Decls[0])
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("expected let stmt, got %#v", fn.Body.Stmts[0])
	}
	cast, ok := letDecl.Value.(*ast.AsExpr)
	if !ok {
		t.Fatalf("expected cast expression, got %#v", letDecl.Value)
	}
	if _, ok := cast.Expr.(*ast.UnaryExpr); !ok {
		t.Fatalf("expected unary expression inside cast, got %#v", cast.Expr)
	}
}

func TestParseMalformedLocalLetDoesNotAppendTypedNilStmt(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = +;
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok || fn.Body == nil {
		t.Fatalf("unexpected function decl: %#v", mod.Decls[0])
	}
	if len(fn.Body.Stmts) != 2 {
		t.Fatalf("stmts: got %d want 2", len(fn.Body.Stmts))
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok || letDecl == nil {
		t.Fatalf("expected non-nil let stmt, got %#v", fn.Body.Stmts[0])
	}
	if letDecl.Value != nil {
		t.Fatalf("expected nil malformed initializer, got %#v", letDecl.Value)
	}
	if _, ok := fn.Body.Stmts[1].(*ast.ReturnStmt); !ok {
		t.Fatalf("expected return stmt after recovery, got %#v", fn.Body.Stmts[1])
	}
}

func TestParseRecoversMissingSemicolonAndContinuesTopLevel(t *testing.T) {
	src := `let a: i32 = 10
let b: i32 = 23;
fn main() -> i32 {
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
	src := `fn main() -> i32 {
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
fn main() -> i32 {
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
fn main() -> i32 {
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
fn main() -> i32 {
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
	src := `fn User::Method(self: i32, other: i32) -> i32 {
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

func TestParseAnonymousTypesInBindings(t *testing.T) {
	src := `let a: struct {
	x: i32,
} = value;
let b: interface {
	call(x: i32): i32,
} = value;
let c: enum {
	One,
	Two,
} = value;`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Decls) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Decls))
	}
	l0 := mod.Decls[0].(*ast.LetDecl)
	if _, ok := l0.Type.(*ast.StructType); !ok {
		t.Fatalf("let a type expected struct")
	}
	l1 := mod.Decls[1].(*ast.LetDecl)
	if _, ok := l1.Type.(*ast.InterfaceType); !ok {
		t.Fatalf("let b type expected interface")
	}
	l2 := mod.Decls[2].(*ast.LetDecl)
	if _, ok := l2.Type.(*ast.EnumType); !ok {
		t.Fatalf("let c type expected enum")
	}
}

func TestParseNestedBlockStmt(t *testing.T) {
	src := `fn main() -> i32 {
{
let a = 1;
return a;
}
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() || mod == nil {
		t.Fatalf("parse failed: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Decls[0])
	}
	if _, ok := fn.Body.Stmts[0].(*ast.BlockStmt); !ok {
		t.Fatalf("expected nested block stmt, got %#v", fn.Body.Stmts[0])
	}
}

func TestParseIfElseStmt(t *testing.T) {
	src := `fn main() -> i32 {
if 1 < 2 {
	return 10;
} else {
	return 20;
}
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() || mod == nil {
		t.Fatalf("parse failed: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Decls[0])
	}
	ifs, ok := fn.Body.Stmts[0].(*ast.IfStmt)
	if !ok {
		t.Fatalf("expected if stmt, got %#v", fn.Body.Stmts[0])
	}
	if ifs.Then == nil || len(ifs.Then.Stmts) != 1 {
		t.Fatalf("missing then block")
	}
	elseBlock, ok := ifs.Else.(*ast.BlockStmt)
	if !ok || len(elseBlock.Stmts) != 1 {
		t.Fatalf("expected else block, got %#v", ifs.Else)
	}
}

func TestParseElseIfStmt(t *testing.T) {
	src := `fn main() -> i32 {
if 1 {
	return 1;
} else if 2 {
	return 2;
} else {
	return 3;
}
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() || mod == nil {
		t.Fatalf("parse failed: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Decls[0])
	}
	root, ok := fn.Body.Stmts[0].(*ast.IfStmt)
	if !ok {
		t.Fatalf("expected if stmt, got %#v", fn.Body.Stmts[0])
	}
	next, ok := root.Else.(*ast.IfStmt)
	if !ok {
		t.Fatalf("expected else-if stmt, got %#v", root.Else)
	}
	if _, ok := next.Else.(*ast.BlockStmt); !ok {
		t.Fatalf("expected final else block, got %#v", next.Else)
	}
}

func TestParseTypeDeclarations(t *testing.T) {
	src := `struct Vec2 {
	x: f32,
	y: f32,
}
interface Adder {
	add(i32, i32): i32,
}
enum Color {
	Red,
	Green,
	Blue,
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Decls) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Decls))
	}
	a0, ok := mod.Decls[0].(*ast.StructDecl)
	if !ok {
		t.Fatalf("decl[0] expected struct decl")
	}
	if len(a0.Fields) != 2 {
		t.Fatalf("struct fields: got %d want 2", len(a0.Fields))
	}
	a1, ok := mod.Decls[1].(*ast.InterfaceDecl)
	if !ok {
		t.Fatalf("decl[1] expected interface decl")
	}
	if len(a1.Methods) != 1 {
		t.Fatalf("interface methods: got %d want 1", len(a1.Methods))
	}
	if got := len(a1.Methods[0].Params); got != 2 {
		t.Fatalf("interface params: got %d want 2", got)
	}
	if a1.Methods[0].Params[0].Name != nil {
		t.Fatalf("first interface param should be unnamed")
	}
	a2, ok := mod.Decls[2].(*ast.EnumDecl)
	if !ok {
		t.Fatalf("decl[2] expected enum decl")
	}
	if len(a2.Variants) != 3 {
		t.Fatalf("enum variants: got %d want 3", len(a2.Variants))
	}
}

func TestParseImplDecl(t *testing.T) {
	src := `impl i32 {
	fn abs(self: Self) -> Self {
		return self;
	}
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
	implDecl, ok := mod.Decls[0].(*ast.ImplDecl)
	if !ok {
		t.Fatalf("decl[0] expected impl decl")
	}
	target, ok := implDecl.Target.(*ast.NamedType)
	if !ok || target.Name != "i32" {
		t.Fatalf("impl target mismatch: %#v", implDecl.Target)
	}
	if len(implDecl.Methods) != 1 {
		t.Fatalf("methods: got %d want 1", len(implDecl.Methods))
	}
	method := implDecl.Methods[0]
	if method.Name == nil || method.Name.Name != "abs" {
		t.Fatalf("method name mismatch: %#v", method.Name)
	}
	if len(method.Params) != 1 || method.Params[0].Name == nil || method.Params[0].Name.Name != "self" {
		t.Fatalf("self parameter not parsed")
	}
}

func TestParseReferenceAndRawPointerTypes(t *testing.T) {
	src := `let shared: &i32;
let unique: &mut i32;
let rawConst: *const i32;
let rawMut: *mut i32;`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Decls) != 4 {
		t.Fatalf("decls: got %d want 4", len(mod.Decls))
	}
	shared, ok := mod.Decls[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("decl[0] expected let")
	}
	ref0, ok := shared.Type.(*ast.RefType)
	if !ok || ref0.Mutable {
		t.Fatalf("decl[0] expected shared ref type, got %#v", shared.Type)
	}
	unique, ok := mod.Decls[1].(*ast.LetDecl)
	if !ok {
		t.Fatalf("decl[1] expected let")
	}
	ref1, ok := unique.Type.(*ast.RefType)
	if !ok || !ref1.Mutable {
		t.Fatalf("decl[1] expected mutable ref type, got %#v", unique.Type)
	}
	rawConst, ok := mod.Decls[2].(*ast.LetDecl)
	if !ok {
		t.Fatalf("decl[2] expected let")
	}
	ptr2, ok := rawConst.Type.(*ast.RawPtrType)
	if !ok || ptr2.Mutable {
		t.Fatalf("decl[2] expected const raw ptr type, got %#v", rawConst.Type)
	}
	rawMut, ok := mod.Decls[3].(*ast.LetDecl)
	if !ok {
		t.Fatalf("decl[3] expected let")
	}
	ptr3, ok := rawMut.Type.(*ast.RawPtrType)
	if !ok || !ptr3.Mutable {
		t.Fatalf("decl[3] expected mutable raw ptr type, got %#v", rawMut.Type)
	}
}

func TestParseBorrowExpressions(t *testing.T) {
	src := `fn main() -> i32 {
	let mut value: i32 = 0;
	let a = &value;
	let b = &mut value;
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	mod := ParseModule("test.em", stream, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Decls[0].(*ast.FnDecl)
	if !ok || fn.Body == nil {
		t.Fatalf("expected function decl, got %#v", mod.Decls[0])
	}
	if len(fn.Body.Stmts) != 4 {
		t.Fatalf("stmts: got %d want 4", len(fn.Body.Stmts))
	}
	aDecl, ok := fn.Body.Stmts[1].(*ast.LetDecl)
	if !ok {
		t.Fatalf("stmt[1] expected let, got %#v", fn.Body.Stmts[1])
	}
	aBorrow, ok := aDecl.Value.(*ast.BorrowExpr)
	if !ok || aBorrow.Mutable {
		t.Fatalf("stmt[1] expected shared borrow, got %#v", aDecl.Value)
	}
	bDecl, ok := fn.Body.Stmts[2].(*ast.LetDecl)
	if !ok {
		t.Fatalf("stmt[2] expected let, got %#v", fn.Body.Stmts[2])
	}
	bBorrow, ok := bDecl.Value.(*ast.BorrowExpr)
	if !ok || !bBorrow.Mutable {
		t.Fatalf("stmt[2] expected mutable borrow, got %#v", bDecl.Value)
	}
}
